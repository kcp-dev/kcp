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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/aojea/rwconn"
	"golang.org/x/net/http2"

	"k8s.io/klog/v2"
)

var _ net.Listener = (*Listener)(nil)

// Listener is a net.Listener, returning new connections which arrive
// from a corresponding Dialer.
type Listener struct {
	url    string
	client *http.Client

	sc     net.Conn // control plane connection
	connc  chan net.Conn
	donec  chan struct{}
	writec chan<- []byte

	mu      sync.Mutex // guards below, closing connc, and writing to rw
	readErr error
	closed  bool
}

// NewListener returns a new Listener, it dials to the Dialer
// creating "reverse connection" that are accepted by this Listener.
// - client: http client, required for TLS
// - url: a URL to the base of the reverse handler on the Dialer
func NewListener(client *http.Client, url string) (*Listener, error) {
	err := configureHTTP2Transport(client)
	if err != nil {
		return nil, err
	}

	ln := &Listener{
		url:    url,
		client: client,
		connc:  make(chan net.Conn, 4), // arbitrary
		donec:  make(chan struct{}),
	}

	// create control plane connection
	// poor man backoff retry
	sleep := 1 * time.Second
	var c net.Conn
	for attempts := 5; attempts > 0; attempts-- {
		c, err = ln.dial()
		if err != nil {
			klog.V(5).Infof("Can not create control connection %v", err)
			// Add some randomness to prevent creating a Thundering Herd
			jitter := time.Duration(rand.Int63n(int64(sleep)))
			sleep = 2*sleep + jitter/2
			time.Sleep(sleep)
		} else {
			ln.sc = c
			break
		}
	}
	if c == nil || err != nil {
		return nil, err
	}

	go ln.run()
	return ln, nil
}

// run establish reverse connections against the server
func (ln *Listener) run() {
	defer ln.Close()

	// Write loop
	writec := make(chan []byte, 8)
	ln.writec = writec
	go func() {
		for {
			select {
			case <-ln.donec:
				return
			case msg := <-writec:
				if _, err := ln.sc.Write(msg); err != nil {
					log.Printf("tunneler.Listener: error writing message to server: %v", err)
					ln.Close()
					return
				}
			}
		}
	}()

	// Read loop
	br := bufio.NewReader(ln.sc)
	for {
		line, err := br.ReadSlice('\n')
		if err != nil {
			return
		}
		var msg controlMsg
		if err := json.Unmarshal(line, &msg); err != nil {
			log.Printf("tunneler.Listener read invalid JSON: %q: %v", line, err)
			return
		}
		switch msg.Command {
		case "keep-alive":
			// Occasional no-op message from server to keep
			// us alive through NAT timeouts.
		case "conn-ready":
			go ln.grabConn()
		default:
			// Ignore unknown messages
		}
	}
}

func (ln *Listener) sendMessage(m controlMsg) {
	j, _ := json.Marshal(m)
	j = append(j, '\n')
	ln.writec <- j
}

func (ln *Listener) dial() (net.Conn, error) {
	connect := ln.url + "/" + cmdTunnelConnect
	pr, pw := io.Pipe()
	req, err := http.NewRequest("GET", connect, pr)
	if err != nil {
		klog.V(5).Infof("Can not create request %v", err)
		return nil, err
	}

	klog.V(5).Infof("Listener creating connection to %s", connect)
	res, err := ln.client.Do(req)
	if err != nil {
		klog.V(5).Infof("Can not connect to %s request %v, retry %d", connect, err)
		return nil, err
	}
	if res.StatusCode != 200 {
		klog.V(5).Infof("Status code %d on request %v, retry %d", res.StatusCode, connect)
		return nil, fmt.Errorf("status code %d", res.StatusCode)
	}

	conn := rwconn.NewConn(res.Body, pw)
	return conn, nil
}

func (ln *Listener) grabConn() {
	// create a new connection
	c, err := ln.dial()
	if err != nil {
		klog.V(5).Infof("Can not create connection %v", err)
		ln.sendMessage(controlMsg{Command: "pickup-failed", ConnPath: "", Err: err.Error()})
		return
	}

	// send the connection to the listener
	select {
	case <-ln.donec:
		return
	default:
		select {
		case ln.connc <- c:
		case <-ln.donec:
			return
		}
	}

}

// Accept blocks and returns a new connection, or an error.
func (ln *Listener) Accept() (net.Conn, error) {
	c, ok := <-ln.connc
	if !ok {
		ln.mu.Lock()
		err, closed := ln.readErr, ln.closed
		ln.mu.Unlock()
		if err != nil && !closed {
			return nil, fmt.Errorf("tunneler: Listener closed; %w", err)
		}
		return nil, ErrListenerClosed
	}
	klog.V(5).Infof("Accept connection")
	return c, nil
}

// ErrListenerClosed is returned by Accept after Close has been called.
var ErrListenerClosed = errors.New("tunneler: Listener closed")

// Close closes the Listener, making future Accept calls return an
// error.
func (ln *Listener) Close() error {
	ln.mu.Lock()
	defer ln.mu.Unlock()
	if ln.closed {
		return nil
	}
	ln.closed = true
	close(ln.connc)
	close(ln.donec)
	ln.sc.Close()
	return nil
}

// Addr returns a dummy address. This exists only to conform to the
// net.Listener interface.
func (ln *Listener) Addr() net.Addr { return connAddr{} }

// configureHTTP2Transport enable ping to avoid issues with stale connections
func configureHTTP2Transport(client *http.Client) error {
	t, ok := client.Transport.(*http.Transport)
	if !ok {
		// can't get the transport it will fail later if not http2 supported
		return nil
	}

	if t.TLSClientConfig == nil {
		return fmt.Errorf("only TLS supported")
	}

	for _, v := range t.TLSClientConfig.NextProtos {
		// http2 already configured
		if v == "h2" {
			return nil
		}
	}

	t2, err := http2.ConfigureTransports(t)
	if err != nil {
		return err
	}

	t2.ReadIdleTimeout = time.Duration(30) * time.Second
	t2.PingTimeout = time.Duration(15) * time.Second
	return nil
}

type connAddr struct{}

func (connAddr) Network() string { return "rwconn" }
func (connAddr) String() string  { return "rwconn" }
