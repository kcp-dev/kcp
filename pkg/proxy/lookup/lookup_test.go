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
	"testing"

	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/index"
)

// fakeIndex is a test double for proxyindex.Index.
type fakeIndex struct {
	result           index.Result
	found            bool
	recentlyMigrated bool

	registered int
	released   int
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

func TestClusterResolveHandlerMigration(t *testing.T) {
	t.Parallel()

	const cluster = "abcdef"

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
				found:            true,
				recentlyMigrated: tt.recentlyMigrated,
				result: index.Result{
					Cluster: logicalcluster.Name(cluster),
					Shard:   "shard-1",
					URL:     "https://shard-1.example/clusters/" + cluster,
				},
			}

			delegateCalled := false
			delegate := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				delegateCalled = true
				w.WriteHeader(http.StatusOK)
			})

			handler := newClusterResolveHandler(delegate, fake)

			query := "?resourceVersion=" + tt.resourceVersion
			if tt.watch {
				query += "&watch=true"
			}
			req := httptest.NewRequest(http.MethodGet, "/clusters/"+cluster+"/api/v1/configmaps"+query, http.NoBody)
			req.SetPathValue("cluster", cluster)
			ctx := request.WithRequestInfo(req.Context(), &request.RequestInfo{
				IsResourceRequest: true,
				Verb:              tt.verb,
				APIVersion:        "v1",
				Resource:          "configmaps",
			})
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

func TestExtractClusterFromPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "virtual workspace URL with cluster",
			path:     "/services/contentconfigurations/clusters/root:orgs:bob/apis/ui.platform-mesh.io/v1alpha1/contentconfigurations",
			expected: "root:orgs:bob",
		},
		{
			name:     "virtual workspace URL with nested cluster path",
			path:     "/services/marketplace/clusters/root:orgs:bob:quickstart/apis/v1/namespaces",
			expected: "root:orgs:bob:quickstart",
		},
		{
			name:     "cluster path only",
			path:     "/clusters/root:orgs:bob/api/v1/namespaces",
			expected: "root:orgs:bob",
		},
		{
			name:     "cluster at end of path without trailing slash",
			path:     "/services/foo/clusters/root:orgs:bob",
			expected: "root:orgs:bob",
		},
		{
			name:     "no cluster in path",
			path:     "/services/contentconfigurations/apis/v1/namespaces",
			expected: "",
		},
		{
			name:     "empty path",
			path:     "",
			expected: "",
		},
		{
			name:     "wildcard cluster",
			path:     "/services/foo/clusters/*/apis/v1",
			expected: "*",
		},
		{
			name:     "root cluster only",
			path:     "/services/foo/clusters/root/apis/v1",
			expected: "root",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := extractClusterFromPath(tt.path)
			if result != tt.expected {
				t.Errorf("extractClusterFromPath(%q) = %q, want %q", tt.path, result, tt.expected)
			}
		})
	}
}
