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

package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNewProxyErrorHandler_ClientDisconnect checks that a client going away is
// not reported as a backend failure. A cancelled watch is ordinary client
// behaviour, and answering a connection that is already gone is pointless.
func TestNewProxyErrorHandler_ClientDisconnect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cancel     bool
		err        error
		wantStatus int
	}{
		{
			name:       "a backend failure is still a 502",
			err:        errors.New("connection refused"),
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "a cancelled request writes no response",
			cancel:     true,
			err:        context.Canceled,
			wantStatus: http.StatusOK, // nothing written, so the recorder keeps its default
		},
		{
			name:       "an outbound cancellation writes no response",
			err:        context.Canceled,
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			if tc.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/clusters/root/api/v1/secrets", http.NoBody)
			rr := httptest.NewRecorder()

			NewProxyErrorHandler()(rr, req, tc.err)

			require.Equal(t, tc.wantStatus, rr.Code)
		})
	}
}
