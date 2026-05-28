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

package filters

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
)

func TestWithShardLevelPaths(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		cluster        *request.Cluster
		wantStatus     int
		wantNextCalled bool
		wantClusterIn  logicalcluster.Name
	}{
		{
			name:           "non-shard path passes through unchanged",
			path:           "/apis/apis.kcp.io/v1alpha1/apiexports",
			cluster:        &request.Cluster{Name: logicalcluster.Name("ws-1234")},
			wantStatus:     http.StatusOK,
			wantNextCalled: true,
			wantClusterIn:  logicalcluster.Name("ws-1234"),
		},
		{
			name:           "metrics with no cluster context scopes to root",
			path:           "/metrics",
			cluster:        nil,
			wantStatus:     http.StatusOK,
			wantNextCalled: true,
			wantClusterIn:  core.RootCluster,
		},
		{
			name:           "metrics with empty cluster name scopes to root",
			path:           "/metrics",
			cluster:        &request.Cluster{},
			wantStatus:     http.StatusOK,
			wantNextCalled: true,
			wantClusterIn:  core.RootCluster,
		},
		{
			name:           "metrics with explicit root cluster passes through",
			path:           "/metrics",
			cluster:        &request.Cluster{Name: core.RootCluster},
			wantStatus:     http.StatusOK,
			wantNextCalled: true,
			wantClusterIn:  core.RootCluster,
		},
		{
			name:           "metrics with workspace cluster is rejected with 501",
			path:           "/metrics",
			cluster:        &request.Cluster{Name: logicalcluster.Name("ws-1234")},
			wantStatus:     http.StatusNotImplemented,
			wantNextCalled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nextCalled := false
			var seenCluster logicalcluster.Name
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				if c := request.ClusterFrom(r.Context()); c != nil {
					seenCluster = c.Name
				}
				w.WriteHeader(http.StatusOK)
			})

			h := WithShardLevelPaths(next)

			req := httptest.NewRequest(http.MethodGet, "https://shard.example"+tc.path, nil)
			if tc.cluster != nil {
				req = req.WithContext(request.WithCluster(req.Context(), *tc.cluster))
			}
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}
			if nextCalled != tc.wantNextCalled {
				t.Errorf("nextCalled: got %v, want %v", nextCalled, tc.wantNextCalled)
			}
			if tc.wantNextCalled && seenCluster != tc.wantClusterIn {
				t.Errorf("cluster passed to next handler: got %q, want %q", seenCluster, tc.wantClusterIn)
			}
		})
	}
}
