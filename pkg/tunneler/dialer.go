/*
Copyright 2022 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tunneler

// Based on https://github.com/golang/build/blob/master/tunneler/v2/tunneler.go

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"k8s.io/klog/v2"
)

// The Dialer can create new connections back to the origin.
// A Dialer can have multiple clients.
type Dialer struct {
	conn         net.Conn      // control plane connection
	incomingConn chan net.Conn // data plane connections
	connReady    chan bool
	pickupFailed chan error
	donec        chan struct{}
	closeOnce    sync.Once
}

// NewDialer returns the side of the connection which will initiate
// new connections over the already established reverse connections.
func NewDialer(conn net.Conn) *Dialer {
	d := &Dialer{
		conn:         conn,
		donec:        make(chan struct{}),
		connReady:    make(chan bool),
		pickupFailed: make(chan error),
		incomingConn: make(chan net.Conn),
	}
	go d.serve()
	return d
}

// serve blocks and runs the control message loop, keeping the peer
// alive and notifying the peer when new connections are available.
func (d *Dialer) serve() {
	defer d.Close()
	go func() {
		defer d.Close()
		br := bufio.NewReader(d.conn)
		for {
			line, err := br.ReadSlice('\n')
			if err != nil {
				return
			}
			select {
			case <-d.donec:
				return
			default:
			}
			var msg controlMsg
			if err := json.Unmarshal(line, &msg); err != nil {
				log.Printf("tunneler.Dialer read invalid JSON: %q: %v", line, err)
				return
			}
			switch msg.Command {
			case "pickup-failed":
				err := fmt.Errorf("tunneler listener failed to pick up connection: %v", msg.Err)
				select {
				case d.pickupFailed <- err:
				case <-d.donec:
					return
				}
			}
		}
	}()
	for {
		if err := d.sendMessage(controlMsg{Command: "keep-alive"}); err != nil {
			return
		}

		t := time.NewTimer(30 * time.Second)
		select {
		case <-t.C:
			continue
		case <-d.connReady:
			if err := d.sendMessage(controlMsg{
				Command:  "conn-ready",
				ConnPath: "",
			}); err != nil {
				return
			}
		case <-d.donec:
			return
		}
	}
}

func (d *Dialer) sendMessage(m controlMsg) error {
	j, _ := json.Marshal(m)
	err := d.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err != nil {
		return err
	}
	j = append(j, '\n')
	_, err = d.conn.Write(j)
	if err != nil {
		return err
	}
	return d.conn.SetWriteDeadline(time.Time{})
}

// Done returns a channel which is closed when d is closed (either by
// this process on purpose, by a local error, or close or error from
// the peer).
func (d *Dialer) Done() <-chan struct{} { return d.donec }

// Close closes the Dialer.
func (d *Dialer) Close() error {
	d.closeOnce.Do(d.close)
	return nil
}

func (d *Dialer) close() {
	d.conn.Close()
	close(d.donec)
}

// Dial creates a new connection back to the Listener.
func (d *Dialer) Dial(ctx context.Context, network string, address string) (net.Conn, error) {
	now := time.Now()
	defer klog.V(5).Infof("dial to %s took %v", address, time.Since(now))
	// First, tell serve that we want a connection:
	select {
	case d.connReady <- true:
	case <-d.donec:
		return nil, errors.New("tunneler.Dialer closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	// Then pick it up:
	select {
	case c := <-d.incomingConn:
		return c, nil
	case err := <-d.pickupFailed:
		return nil, err
	case <-d.donec:
		return nil, errors.New("tunneler.Dialer closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
