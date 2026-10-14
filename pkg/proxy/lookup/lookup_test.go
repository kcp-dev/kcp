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

package lookup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/index"
	"github.com/kcp-dev/kcp/pkg/server/proxy/types"
)

// fakeIndex is a test double for index.Index.
type fakeIndex struct {
	result           index.Result
	found            bool
	baseURL          string
	recentlyMigrated bool

	registered int
	released   int
}

func (f *fakeIndex) BaseURL(shardName string) (string, bool) {
	return f.baseURL, f.baseURL != ""
}

func (f *fakeIndex) Lookup(path logicalcluster.Path) (index.Result, bool) {
	return f.result, f.found
}

func (f *fakeIndex) LookupURL(path logicalcluster.Path) (index.Result, bool) {
	return f.result, f.found
}

func (f *fakeIndex) RecentlyMigrated(cluster logicalcluster.Name) bool {
	return f.recentlyMigrated
}

func (f *fakeIndex) ClusterContext(parent context.Context, cluster logicalcluster.Name) (context.Context, context.CancelFunc) {
	f.registered++
	ctx, cancel := context.WithCancel(parent)
	return ctx, func() {
		f.released++
		cancel()
	}
}

func TestShardHandlerMigration(t *testing.T) {
	t.Parallel()

	const clusterID = "abcdef"

	tests := []struct {
		name             string
		recentlyMigrated bool
		verb             string
		watch            bool
		resourceVersion  string
		wantStatus       int
		wantDelegate     bool
		wantRegistered   bool
	}{
		{
			name:             "migrated, watch with stale resourceVersion -> 410",
			recentlyMigrated: true,
			verb:             "watch",
			watch:            true,
			resourceVersion:  "8326",
			wantStatus:       http.StatusGone,
			wantDelegate:     false,
			wantRegistered:   false,
		},
		{
			name:             "migrated, list with stale resourceVersion -> forwarded (lists self-heal)",
			recentlyMigrated: true,
			verb:             "list",
			resourceVersion:  "8326",
			wantStatus:       http.StatusOK,
			wantDelegate:     true,
			wantRegistered:   false,
		},
		{
			name:             "migrated, watch without resourceVersion -> forwarded (relist allowed)",
			recentlyMigrated: true,
			verb:             "watch",
			watch:            true,
			resourceVersion:  "",
			wantStatus:       http.StatusOK,
			wantDelegate:     true,
			wantRegistered:   true,
		},
		{
			name:             "migrated, list with resourceVersion=0 -> forwarded",
			recentlyMigrated: true,
			verb:             "list",
			resourceVersion:  "0",
			wantStatus:       http.StatusOK,
			wantDelegate:     true,
			wantRegistered:   false,
		},
		{
			name:             "migrated, get with resourceVersion -> forwarded (not list/watch)",
			recentlyMigrated: true,
			verb:             "get",
			resourceVersion:  "8326",
			wantStatus:       http.StatusOK,
			wantDelegate:     true,
			wantRegistered:   false,
		},
		{
			name:             "not migrated, watch with resourceVersion -> forwarded + registered",
			recentlyMigrated: false,
			verb:             "watch",
			watch:            true,
			resourceVersion:  "8326",
			wantStatus:       http.StatusOK,
			wantDelegate:     true,
			wantRegistered:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeIndex{
				baseURL:          "https://shard-1.example",
				recentlyMigrated: tt.recentlyMigrated,
			}

			delegateCalled := false
			delegate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				delegateCalled = true
				w.WriteHeader(http.StatusOK)
			})

			handler := newShardHandler(delegate, fake)

			query := "?resourceVersion=" + tt.resourceVersion
			if tt.watch {
				query += "&watch=true"
			}
			// URL as left by WithClusterResolution: /clusters/<segment> stripped.
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/configmaps"+query, http.NoBody)
			ctx := request.WithRequestInfo(req.Context(), &request.RequestInfo{
				IsResourceRequest: true,
				Verb:              tt.verb,
				APIVersion:        "v1",
				Resource:          "configmaps",
			})
			ctx = request.WithCluster(ctx, request.Cluster{Name: clusterID})
			ctx = WithShardName(ctx, "shard-1")
			req = req.WithContext(ctx)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if delegateCalled != tt.wantDelegate {
				t.Errorf("delegate called = %v, want %v", delegateCalled, tt.wantDelegate)
			}
			if (fake.registered > 0) != tt.wantRegistered {
				t.Errorf("registered = %d, want registered=%v", fake.registered, tt.wantRegistered)
			}
			// Any registered in-flight watch must be released when the request finishes.
			if fake.registered != fake.released {
				t.Errorf("registered (%d) != released (%d): in-flight watch not released", fake.registered, fake.released)
			}
		})
	}
}

func TestShardHandlerURL(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		url string

		wantStatus   int
		wantShardURL string
	}{
		"cluster ID": {
			url:          "/clusters/" + wsClusterID + "/api/v1/configmaps?watch=true",
			wantShardURL: "https://beta.example.com/clusters/" + wsClusterID + "/api/v1/configmaps?watch=true",
		},
		"workspace path": {
			url:          "/clusters/" + wsWorkspacePath + "/api/v1",
			wantShardURL: "https://beta.example.com/clusters/" + wsClusterID + "/api/v1",
		},
		"workspace path without trailing path": {
			url:          "/clusters/" + orgWorkspacePath,
			wantShardURL: "https://root.example.com/clusters/" + orgClusterID,
		},
		"mount to parent shard": {
			url:          "/clusters/" + mntWorkspacePath + "/api/v1",
			wantShardURL: "https://root.example.com/clusters/" + orgClusterID + ":mnt/api/v1",
		},
		"wildcard has no shard": {
			url:        "/clusters/*/api/v1",
			wantStatus: http.StatusForbidden,
		},
		"unknown workspace path": {
			url:        "/clusters/root:nope/api/v1",
			wantStatus: http.StatusForbidden,
		},
	}

	idx := newResolveIndex()

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var shardURL *url.URL
			delegate := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				shardURL = ShardURLFrom(req.Context())
			})
			handler := WithClusterResolution(newShardHandler(delegate, idx), idx)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.url, http.NoBody))

			if tc.wantStatus != 0 {
				require.Nil(t, shardURL)
				require.Equal(t, tc.wantStatus, rec.Code)
				return
			}
			require.NotNil(t, shardURL)
			require.Equal(t, tc.wantShardURL, shardURL.String())
		})
	}
}

func TestWithClusterResolver(t *testing.T) {
	t.Parallel()

	mappings := []types.PathMapping{
		{Path: "/clusters/"},
		{Path: "/services/singleton/{cluster}"},
		{Path: "/services/org/clusters/{cluster}"},
		{Path: "/services/plain"},
		{Path: "/"},
	}

	tests := map[string]struct {
		url string

		wantStatus   int
		wantShardURL string
		wantCluster  *request.Cluster
		wantURL      string
	}{
		"workspace path to shard": {
			url:          "/clusters/" + wsWorkspacePath + "/api/v1",
			wantShardURL: "https://beta.example.com/clusters/" + wsClusterID + "/api/v1",
			wantCluster:  &request.Cluster{Name: wsClusterID},
			wantURL:      "/api/v1",
		},
		"clusterID to shard": {
			url:          "/clusters/" + wsClusterID + "/api/v1",
			wantShardURL: "https://beta.example.com/clusters/" + wsClusterID + "/api/v1",
			wantCluster:  &request.Cluster{Name: wsClusterID},
			wantURL:      "/api/v1",
		},
		"unknown cluster to shard": {
			url:        "/clusters/root:nope/api/v1",
			wantStatus: http.StatusForbidden,
		},
		"service with cluster placeholder": {
			url:         "/services/singleton/" + wsWorkspacePath + "/api/v1",
			wantCluster: &request.Cluster{Name: wsClusterID},
			wantURL:     "/services/singleton/" + wsWorkspacePath + "/api/v1",
		},
		"service with default /clusters/x": {
			url:         "/services/org/clusters/" + wsWorkspacePath + "/api/v1",
			wantCluster: &request.Cluster{Name: wsClusterID},
			wantURL:     "/services/org/clusters/" + wsWorkspacePath + "/api/v1",
		},
		"service with unknown cluster": {
			url:        "/services/org/clusters/root:nope/api/v1",
			wantStatus: http.StatusForbidden,
		},
		"service without cluster placeholder": {
			url:     "/services/plain/api/v1",
			wantURL: "/services/plain/api/v1",
		},
		"unmatched goes to delegate": {
			url:     "/healthz",
			wantURL: "/healthz",
		},
	}

	idx := newResolveIndex()

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var (
				called   bool
				gotURL   string
				cluster  *request.Cluster
				shardURL *url.URL
			)
			delegate := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				called = true
				gotURL = req.URL.String()
				cluster = request.ClusterFrom(req.Context())
				shardURL = ShardURLFrom(req.Context())
			})

			rec := httptest.NewRecorder()
			WithClusterResolver(delegate, mappings, idx).
				ServeHTTP(rec, httptest.NewRequestWithContext(
					t.Context(),
					http.MethodGet,
					tc.url,
					http.NoBody,
				))

			if tc.wantStatus != 0 {
				require.False(t, called)
				require.Equal(t, tc.wantStatus, rec.Code)
				return
			}

			require.True(t, called)
			require.Equal(t, tc.wantURL, gotURL)
			require.Equal(t, tc.wantCluster, cluster)
			if tc.wantShardURL == "" {
				require.Nil(t, shardURL)
				return
			}
			require.NotNil(t, shardURL)
			require.Equal(t, tc.wantShardURL, shardURL.String())
		})
	}
}
