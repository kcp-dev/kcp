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

	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
)

func TestWithShardScopePassesThroughMetrics(t *testing.T) {
	t.Parallel()
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	h := WithShardScope(next)

	req := httptest.NewRequest(http.MethodGet, "https://cache.example/metrics", http.NoBody)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !nextCalled {
		t.Fatalf("next handler should be called for top-level /metrics on cache server")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestWithCacheShardLevelPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		path           string
		shard          request.Shard
		cluster        *request.Cluster
		wantStatus     int
		wantNextCalled bool
	}{
		{
			name:           "non-shard path passes through",
			path:           "/apis/apis.kcp.io/v1alpha1/apiexports",
			shard:          request.Shard("amber"),
			cluster:        &request.Cluster{Name: logicalcluster.Name("ws-1234")},
			wantStatus:     http.StatusOK,
			wantNextCalled: true,
		},
		{
			name:           "metrics with no shard and no cluster passes through",
			path:           "/metrics",
			wantStatus:     http.StatusOK,
			wantNextCalled: true,
		},
		{
			name:           "metrics with shard set is rejected with 501",
			path:           "/metrics",
			shard:          request.Shard("amber"),
			wantStatus:     http.StatusNotImplemented,
			wantNextCalled: false,
		},
		{
			name:           "metrics with cluster set is rejected with 501",
			path:           "/metrics",
			cluster:        &request.Cluster{Name: logicalcluster.Name("ws-1234")},
			wantStatus:     http.StatusNotImplemented,
			wantNextCalled: false,
		},
		{
			name:           "metrics with both shard and cluster set is rejected",
			path:           "/metrics",
			shard:          request.Shard("amber"),
			cluster:        &request.Cluster{Name: logicalcluster.Name("ws-1234")},
			wantStatus:     http.StatusNotImplemented,
			wantNextCalled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusOK)
			})

			h := WithCacheShardLevelPaths(next)

			req := httptest.NewRequest(http.MethodGet, "https://cache.example"+tc.path, http.NoBody)
			ctx := req.Context()
			if !tc.shard.Empty() {
				ctx = request.WithShard(ctx, tc.shard)
			}
			if tc.cluster != nil {
				ctx = request.WithCluster(ctx, *tc.cluster)
			}
			req = req.WithContext(ctx)
			rec := httptest.NewRecorder()

			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}
			if nextCalled != tc.wantNextCalled {
				t.Errorf("nextCalled: got %v, want %v", nextCalled, tc.wantNextCalled)
			}
		})
	}
}
