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

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/kcp/pkg/mounts"
)

// TestWithRejectMountForwardedRequests makes sure the cache server cannot be used
// as the target of a workspace mount. It serves kcp's replicated state to shards,
// never workspace content to users, so a request that came through a mount proxy
// is refused before anything inspects its identity headers.
func TestWithRejectMountForwardedRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		hops       []string
		wantStatus int
		wantServed bool
	}{
		{
			name:       "an ordinary request is served",
			wantStatus: http.StatusTeapot,
			wantServed: true,
		},
		{
			name:       "a mount-forwarded request is refused",
			hops:       []string{"1"},
			wantStatus: http.StatusBadRequest,
		},
		{
			// The marker alone is disqualifying, whatever it says: no request to
			// the cache server should ever carry it.
			name:       "the marker is refused even when it is not a number",
			hops:       []string{"nonsense"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "the marker is refused even when empty",
			hops:       []string{""},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			served := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				served = true
				w.WriteHeader(http.StatusTeapot)
			})

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/services/cache/shards/root/clusters/x/apis/apis.kcp.io/v1alpha1/apiexports", http.NoBody)
			for _, v := range tc.hops {
				req.Header.Add(mounts.HopsHeader, v)
			}
			rr := httptest.NewRecorder()
			WithRejectMountForwardedRequests(next).ServeHTTP(rr, req)

			require.Equal(t, tc.wantStatus, rr.Code, "body=%s", rr.Body.String())
			require.Equal(t, tc.wantServed, served)
		})
	}
}
