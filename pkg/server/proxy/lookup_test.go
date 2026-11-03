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

package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/index/indextest"
	apiexportbuilder "github.com/kcp-dev/kcp/pkg/virtual/apiexport/builder"
)

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

			fake := &indextest.Index{
				Clusters:                 map[logicalcluster.Name]string{cluster: "shard-1"},
				Shards:                   map[string]string{"shard-1": "https://shard-1.example"},
				RecentlyMigratedClusters: map[logicalcluster.Name]bool{cluster: tt.recentlyMigrated},
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
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/clusters/"+cluster+"/api/v1/configmaps"+query, http.NoBody)
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
			registered, released := fake.Registered.Load(), fake.Released.Load()
			if (registered > 0) != tt.wantRegistered {
				t.Errorf("registered = %d, want registered=%v", registered, tt.wantRegistered)
			}
			// Any registered in-flight watch must be released when the request finishes.
			if registered != released {
				t.Errorf("registered (%d) != released (%d): in-flight watch not released", registered, released)
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
		{
			name:     "replication VW, cached resource named clusters",
			path:     "/services/replication/1oget0q1249b2vcy/clusters/clusters/385doly4poks8a45/apis/wildwest.dev/v1alpha1/clusters",
			expected: "385doly4poks8a45",
		},
		{
			name:     "replication VW, cached resource named clusters, at the cluster's root",
			path:     "/services/replication/1oget0q1249b2vcy/clusters/clusters/385doly4poks8a45",
			expected: "385doly4poks8a45",
		},
		{
			name:     "replication VW, cached resource named clusters, non-API endpoint",
			path:     "/services/replication/1oget0q1249b2vcy/clusters/clusters/385doly4poks8a45/healthz",
			expected: "385doly4poks8a45",
		},
		{
			name:     "apiexport VW, export named clusters",
			path:     "/services/apiexport/root:providers/clusters/clusters/root:orgs:bob/api/v1/namespaces",
			expected: "root:orgs:bob",
		},
		{
			name:     "apiexport VW, export named clusters, wildcard cluster",
			path:     "/services/apiexport/root:providers/clusters/clusters/*/apis/wildwest.dev/v1alpha1/clusters",
			expected: "*",
		},
		{
			name:     "apiexport VW, export named clusters, trailing slash after the cluster",
			path:     "/services/apiexport/root:providers/clusters/clusters/root:orgs:bob/",
			expected: "root:orgs:bob",
		},
		{
			name:     "apiexport VW, export and its workspace both named clusters",
			path:     "/services/apiexport/clusters/clusters/clusters/root:orgs:bob/apis/wildwest.dev/v1alpha1/clusters",
			expected: "root:orgs:bob",
		},
		{
			name:     "apiexport VW, a later clusters segment does not move the cluster",
			path:     "/services/apiexport/root:providers/export/clusters/root:orgs:a/x/clusters/root:orgs:b/apis/v1",
			expected: "root:orgs:a",
		},
		{
			name:     "apiexport VW without a clusters segment where its layout puts one",
			path:     "/services/apiexport/root:providers/export/notclusters/root:orgs:bob/apis/v1",
			expected: "",
		},
		{
			name:     "apiexport VW cut short before its cluster",
			path:     "/services/apiexport/root:providers/export/clusters",
			expected: "",
		},
		{
			name:     "initializingworkspaces VW",
			path:     "/services/initializingworkspaces/root:org:custom/clusters/2x7k9fqp1abcd/apis/core.kcp.io/v1alpha1/logicalclusters",
			expected: "2x7k9fqp1abcd",
		},
		{
			name:     "terminatingworkspaces VW",
			path:     "/services/terminatingworkspaces/root:org:custom/clusters/2x7k9fqp1abcd/apis/core.kcp.io/v1alpha1/logicalclusters",
			expected: "2x7k9fqp1abcd",
		},
		{
			name:     "apiresourceschema VW",
			path:     "/services/apiresourceschema/root:consumer/clusters/*/apis/apis.kcp.io/v1alpha1/apiresourceschemas",
			expected: "*",
		},
		{
			name:     "admin VW",
			path:     "/services/admin/clusters/root:orgs:bob/apis/core.kcp.io/v1alpha1/logicalclusters",
			expected: "root:orgs:bob",
		},
		{
			name:     "migratingworkspaces VW",
			path:     "/services/migratingworkspaces/clusters/2x7k9fqp1abcd/apis/v1",
			expected: "2x7k9fqp1abcd",
		},
		{
			name:     "unknown VW, resource named clusters after the cluster",
			path:     "/services/foo/clusters/root:orgs:bob/apis/wildwest.dev/v1alpha1/clusters/cluster-a",
			expected: "root:orgs:bob",
		},
		{
			name:     "unknown VW keeps the first segment after /clusters/, even an empty one",
			path:     "/services/foo/clusters//clusters/root:orgs:bob/apis/v1",
			expected: "",
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

func TestAPIExportVirtualWorkspaceName(t *testing.T) {
	t.Parallel()
	if apiExportVirtualWorkspaceName != apiexportbuilder.VirtualWorkspaceName {
		t.Errorf("apiExportVirtualWorkspaceName = %q, want the apiexport virtual workspace's name %q", apiExportVirtualWorkspaceName, apiexportbuilder.VirtualWorkspaceName)
	}
}
