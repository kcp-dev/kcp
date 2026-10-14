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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	userinfo "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/index"
	"github.com/kcp-dev/kcp/pkg/proxy/lookup"
)

// TestWithLocalProxy_UnresolvablePathIsRejected is a regression test for an
// etcd-corruption bug where WithLocalProxy's fallback for an unresolvable
// multi-segment cluster path ("root:internal-cluster") used to stuff the raw
// path string into request.Cluster.Name and forward the request. The storage
// key builder (NoNamespaceKeyRootFunc) then concatenated the path verbatim
// into the etcd key, producing orphaned rows under e.g.
// /registry/<group>/<resource>/customresources/root:internal-cluster/...
// that are invisible to the normal read path but still consume etcd space.
//
// Such requests are rejected with 403 before they ever reach the
// downstream handler chain.
func TestWithLocalProxy_UnresolvablePathIsRejected(t *testing.T) {
	t.Parallel()
	called := false
	downstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		called = true
		// If we ever get here, verify at least that the poisoned cluster.Name
		// did not make it onto the context.
		if cluster := request.ClusterFrom(req.Context()); cluster != nil {
			if got := cluster.Name.String(); got == "root:internal-cluster" {
				t.Errorf("downstream received poisoned cluster.Name=%q (the bug is back)", got)
			}
		}
		w.WriteHeader(http.StatusOK)
	})

	// An empty index: any path lookup returns found=false, which is exactly
	// the race condition (cold informer / wrong shard / deleted workspace)
	// that the original bug exploited.
	h := newLocalProxy(t, downstream, index.New(nil))

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/clusters/root:internal-cluster/apis/networking.dev/v1/namespaces/proj-x/ipallocations/foo",
		http.NoBody)
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code,
		"expected 403 for unresolvable workspace path, got %d; body=%s", rr.Code, rr.Body.String())
	require.False(t, called,
		"downstream handler must NOT be invoked for an unresolvable workspace path; the request must be rejected before it can reach storage")
}

// TestWithLocalProxy_ClusterIDIsForwarded: a request by cluster ID for a
// cluster on this shard is forwarded with cluster ID, workspace type and
// stripped path.
func TestWithLocalProxy_ClusterIDIsForwarded(t *testing.T) {
	t.Parallel()
	const clusterID = "25t3xbr5iceb0155"

	var (
		gotCluster *request.Cluster
		gotType    logicalcluster.Path
		gotPath    string
	)
	downstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotCluster = request.ClusterFrom(req.Context())
		gotType = lookup.WorkspaceTypeFrom(req.Context())
		gotPath = req.URL.Path
		w.WriteHeader(http.StatusOK)
	})

	idx := index.New(nil)
	idx.UpsertLogicalCluster("test-shard", &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{Name: corev1alpha1.LogicalClusterName, Annotations: map[string]string{
			logicalcluster.AnnotationKey:                    clusterID,
			tenancyv1alpha1.LogicalClusterTypeAnnotationKey: "root:universal",
		}},
	})
	h := newLocalProxy(t, downstream, idx)

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/clusters/"+clusterID+"/apis/networking.dev/v1/namespaces/proj-x/ipallocations/foo",
		http.NoBody)
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body=%s", rr.Body.String())
	require.Equal(t, &request.Cluster{Name: clusterID}, gotCluster)
	require.Equal(t, logicalcluster.NewPath("root:universal"), gotType)
	require.Equal(t, "/apis/networking.dev/v1/namespaces/proj-x/ipallocations/foo", gotPath)
}

// TestWithLocalProxy_UnknownClusterIDIsRejected: a shard serves only the
// logical clusters in its local index.
func TestWithLocalProxy_UnknownClusterIDIsRejected(t *testing.T) {
	t.Parallel()
	downstream := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("a request for an unknown cluster must not continue into the handler chain")
	})

	h := newLocalProxy(t, downstream, index.New(nil))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/clusters/25t3xbr5iceb0155/api/v1", http.NoBody)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "body=%s", rr.Body.String())
}

// TestWithLocalProxy_NonClusterRequestIsForwarded: requests without a
// /clusters/ prefix get an empty cluster and pass through.
func TestWithLocalProxy_NonClusterRequestIsForwarded(t *testing.T) {
	t.Parallel()
	var gotCluster *request.Cluster
	downstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotCluster = request.ClusterFrom(req.Context())
		w.WriteHeader(http.StatusOK)
	})

	h := newLocalProxy(t, downstream, index.New(nil))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", http.NoBody))

	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, &request.Cluster{}, gotCluster)
}

// TestWithProxyAuthHeaders_StripsForgedIdentityHeaders is the regression test for
// the local-proxy identity-header cleaning.
func TestWithProxyAuthHeaders_StripsForgedIdentityHeaders(t *testing.T) {
	t.Parallel()
	const (
		userHeader  = "X-Remote-User"
		groupHeader = "X-Remote-Group"
		extraPrefix = "X-Remote-Extra-"

		warrantHeader = "X-Remote-Extra-Authorization.kcp.io%2fwarrant"
		scopesHeader  = "X-Remote-Extra-Authentication.kcp.io%2fscopes"
	)
	forgedWarrant := `{"user":"attacker","groups":["system:masters"]}`

	tests := []struct {
		name             string
		user             userinfo.Info
		clientHeaders    http.Header
		wantUser         []string
		wantGroups       []string
		forbiddenHeaders []string
	}{
		{
			name: "forged group dropped, real identity stamped",
			user: &userinfo.DefaultInfo{Name: "alice", Groups: []string{"system:authenticated"}},
			clientHeaders: http.Header{
				groupHeader: {"system:masters"},
			},
			wantUser:   []string{"alice"},
			wantGroups: []string{"system:authenticated"},
		},
		{
			name: "forged user overwritten, forged warrant/scopes dropped (no X-Remote-User sent)",
			user: &userinfo.DefaultInfo{Name: "alice", Groups: []string{"system:authenticated"}},
			clientHeaders: http.Header{
				groupHeader:   {"system:masters"},
				warrantHeader: {forgedWarrant},
				scopesHeader:  {"cluster:root"},
			},
			wantUser:         []string{"alice"},
			wantGroups:       []string{"system:authenticated"},
			forbiddenHeaders: []string{warrantHeader, scopesHeader},
		},
		{
			name: "real extras stamped, forged extras dropped",
			user: &userinfo.DefaultInfo{
				Name:   "alice",
				Groups: []string{"system:authenticated"},
				Extra:  map[string][]string{"authentication.kcp.io/cluster-name": {"root:org:ws"}},
			},
			clientHeaders: http.Header{
				warrantHeader: {forgedWarrant},
			},
			wantUser:         []string{"alice"},
			wantGroups:       []string{"system:authenticated"},
			forbiddenHeaders: []string{warrantHeader},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var forwarded http.Header
			sink := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded = r.Header.Clone()
			})
			handler := withProxyAuthHeaders(sink, userHeader, groupHeader, extraPrefix)

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://shard/clusters/root:org:ws/api/v1/secrets", http.NoBody)
			for k, vs := range tc.clientHeaders {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			req = req.WithContext(request.WithUser(req.Context(), tc.user))
			handler.ServeHTTP(httptest.NewRecorder(), req)

			require.Equal(t, tc.wantUser, forwarded.Values(userHeader), "user header")
			require.Equal(t, tc.wantGroups, forwarded.Values(groupHeader), "group header")
			require.NotContains(t, forwarded.Values(groupHeader), "system:masters", "forged group must not leak to backend")
			for _, h := range tc.forbiddenHeaders {
				require.Empty(t, forwarded.Values(h), "forged header %q must not leak to backend", h)
			}
		})
	}
}
