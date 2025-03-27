package portforward

// Copyright (c) Microsoft Corporation.
// Licensed under the Apache License 2.0.

import (
	"errors"
	"io"
	"net"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"
)

type addr struct{}

func (addr) Network() string { return "streamconn" }
func (addr) String() string  { return "streamconn" }

// streamConn wraps a pair of SPDY streams and pretends to be a net.Conn
type streamConn struct {
	c           httpstream.Connection
	dataStream  httpstream.Stream
	errorStream httpstream.Stream
	errch       chan error
}

var _ net.Conn = (*streamConn)(nil)

func NewStreamConn(c httpstream.Connection, dataStream, errorStream httpstream.Stream) net.Conn {
	s := &streamConn{
		errch:       make(chan error, 1),
		c:           c,
		dataStream:  dataStream,
		errorStream: errorStream,
	}

	go s.readErrorStream()

	return s
}

func (s *streamConn) readErrorStream() {
	message, err := io.ReadAll(s.errorStream)
	if err != nil {
		s.errch <- err
	} else if len(message) > 0 {
		s.errch <- errors.New(string(message))
	} else {
		s.errch <- nil
	}
	close(s.errch)
}

func (s *streamConn) Read(b []byte) (int, error) {
	n, err := s.dataStream.Read(b)
	if err == io.EOF {
		err = <-s.errch
		if err == nil {
			err = io.EOF
		}
	}

	return n, err
}

func (s *streamConn) Write(b []byte) (int, error) {
	return s.dataStream.Write(b)
}

func (s *streamConn) Close() error {
	return s.c.Close()
}

func (s *streamConn) LocalAddr() net.Addr              { return &addr{} }
func (s *streamConn) RemoteAddr() net.Addr             { return &addr{} }
func (s *streamConn) SetDeadline(time.Time) error      { return errors.New("not implemented") }
func (s *streamConn) SetReadDeadline(time.Time) error  { return errors.New("not implemented") }
func (s *streamConn) SetWriteDeadline(time.Time) error { return errors.New("not implemented") }
