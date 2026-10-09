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
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
)

func TestWithShardLevelPaths(t *testing.T) {
	t.Parallel()
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
			name:           "metrics with explicit root cluster is rejected (use the bare URL)",
			path:           "/metrics",
			cluster:        &request.Cluster{Name: core.RootCluster},
			wantStatus:     http.StatusNotImplemented,
			wantNextCalled: false,
		},
		{
			name:           "metrics with workspace cluster is rejected with 501",
			path:           "/metrics",
			cluster:        &request.Cluster{Name: logicalcluster.Name("ws-1234")},
			wantStatus:     http.StatusNotImplemented,
			wantNextCalled: false,
		},
		{
			// Probes are intentionally NOT in shardpaths so workspace-scoped
			// liveness probing keeps working.
			name:           "livez with workspace cluster passes through unchanged",
			path:           "/livez",
			cluster:        &request.Cluster{Name: logicalcluster.Name("ws-1234")},
			wantStatus:     http.StatusOK,
			wantNextCalled: true,
			wantClusterIn:  logicalcluster.Name("ws-1234"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
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

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://shard.example"+tc.path, http.NoBody)
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

func TestWithShardLevelPaths_MountPassesThrough(t *testing.T) {
	t.Parallel()

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		if cluster := request.ClusterFrom(r.Context()); cluster != nil {
			t.Errorf("a mount request must not be scoped to a cluster, got %q", cluster.Name)
		}
		w.WriteHeader(http.StatusOK)
	})

	target, err := url.Parse("https://mount.example.com")
	require.NoError(t, err)
	ctx := WithMountTarget(t.Context(), &MountTarget{URL: target, Workspace: logicalcluster.NewPath("root:mnt"), ParentCluster: core.RootCluster})
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/metrics", http.NoBody)
	rr := httptest.NewRecorder()
	WithShardLevelPaths(next).ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	require.True(t, nextCalled, "a shard-level path on a mounted workspace belongs to the mount target")
}

func TestWithMountBypass(t *testing.T) {
	t.Parallel()

	var muxCalled, chainCalled bool
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { muxCalled = true })
	chain := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { chainCalled = true })
	h := WithMountBypass(mux, chain)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/services/foo", http.NoBody)
	h.ServeHTTP(httptest.NewRecorder(), req)
	require.True(t, muxCalled, "non-mount requests go through the mux")
	require.False(t, chainCalled)

	muxCalled, chainCalled = false, false
	target, err := url.Parse("https://mount.example.com")
	require.NoError(t, err)
	ctx := WithMountTarget(t.Context(), &MountTarget{URL: target, Workspace: logicalcluster.NewPath("root:mnt"), ParentCluster: core.RootCluster})
	req = httptest.NewRequestWithContext(ctx, http.MethodGet, "/services/foo", http.NoBody)
	h.ServeHTTP(httptest.NewRecorder(), req)
	require.True(t, chainCalled, "mount requests bypass the mux, even when the remaining path matches a registered prefix")
	require.False(t, muxCalled)
}
