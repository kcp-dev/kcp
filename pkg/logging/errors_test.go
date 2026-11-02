/*
Copyright 2026 The kcp Authors.

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

package logging

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
)

func TestIsClientDisconnect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// cancelled builds a cancelled request context, standing in for a client
		// that hung up. nilCtx covers callers that have no context to hand.
		cancelled bool
		nilCtx    bool
		err       error
		want      bool
	}{
		{name: "nil error on a live request"},
		{name: "a real backend failure", err: errors.New("connection refused")},
		{
			// A slow backend is a genuine fault worth an error log, unlike a client
			// that walked away.
			name: "backend deadline exceeded",
			err:  context.DeadlineExceeded,
		},
		{name: "cancelled request context", cancelled: true, err: errors.New("some transport error"), want: true},
		{name: "cancelled outbound request", err: context.Canceled, want: true},
		{name: "wrapped cancellation", err: fmt.Errorf("proxy: %w", context.Canceled), want: true},
		{name: "aborted handler", err: http.ErrAbortHandler, want: true},
		{name: "broken pipe", err: &net.OpError{Err: syscall.EPIPE}, want: true},
		{name: "connection reset by peer", err: &net.OpError{Err: syscall.ECONNRESET}, want: true},
		{name: "nil context is tolerated", nilCtx: true, err: context.Canceled, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			switch {
			case tc.nilCtx:
				ctx = nil
			case tc.cancelled:
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			require.Equal(t, tc.want, IsClientDisconnect(ctx, tc.err))
		})
	}
}

func TestReverseProxyErrorLog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		msg       string
		wantError bool
	}{
		{name: "watch timed out mid-stream", msg: "httputil: ReverseProxy read error during body copy: context deadline exceeded"},
		{name: "client cancelled", msg: "httputil: ReverseProxy read error during body copy: context canceled"},
		{name: "client went away before the backend answered", msg: "http: proxy error: context canceled"},
		{
			// The backend reset the connection, which is a backend fault.
			name:      "backend reset the connection",
			msg:       "httputil: ReverseProxy read error during body copy: read tcp 10.0.0.1:443: read: connection reset by peer",
			wantError: true,
		},
		{name: "real failure", msg: "httputil: ReverseProxy read error during body copy: unexpected EOF", wantError: true},
		{
			// A deadline before any response may be a hung backend.
			name:      "backend timed out before answering",
			msg:       "http: proxy error: context deadline exceeded",
			wantError: true,
		},
		{
			// The same error text under another prefix is not demoted.
			name:      "unrelated message mentioning a deadline",
			msg:       "suppressing panic for copyResponse error in test; copy error: context deadline exceeded",
			wantError: true,
		},
		{name: "unknown proxy error", msg: "http: proxy error: dial tcp 10.0.0.1:443: connect: connection refused", wantError: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sink := &recordingSink{}
			NewReverseProxyErrorLog(logr.New(sink)).Print(tc.msg)

			require.Equal(t, []string{tc.msg}, sink.msgs)
			require.Equal(t, tc.wantError, sink.errors == 1)
			if !tc.wantError {
				require.Equal(t, []int{4}, sink.levels)
			}
		})
	}
}

type recordingSink struct {
	msgs   []string
	levels []int
	errors int
}

func (s *recordingSink) Init(logr.RuntimeInfo)          {}
func (s *recordingSink) Enabled(int) bool               { return true }
func (s *recordingSink) WithValues(...any) logr.LogSink { return s }
func (s *recordingSink) WithName(string) logr.LogSink   { return s }
func (s *recordingSink) Info(level int, msg string, _ ...any) {
	s.msgs = append(s.msgs, msg)
	s.levels = append(s.levels, level)
}
func (s *recordingSink) Error(_ error, msg string, _ ...any) {
	s.msgs = append(s.msgs, msg)
	s.errors++
}
