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
