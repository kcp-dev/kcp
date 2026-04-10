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

	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/index"
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
// The fix rejects such requests with 404 before they ever reach the
// downstream handler chain.
func TestWithLocalProxy_UnresolvablePathIsRejected(t *testing.T) {
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
	emptyIndex := index.New(nil)

	h, err := WithLocalProxy(downstream, "test-shard", "", emptyIndex)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet,
		"/clusters/root:internal-cluster/apis/networking.dev/v1/namespaces/proj-x/ipallocations/foo",
		http.NoBody)
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code,
		"expected 404 for unresolvable workspace path, got %d; body=%s", rr.Code, rr.Body.String())
	require.False(t, called,
		"downstream handler must NOT be invoked for an unresolvable workspace path; the request must be rejected before it can reach storage")
}

// TestWithLocalProxy_BareNameIsForwarded makes sure the happy path is
// unchanged: a single-segment cluster name (a hash) is forwarded through to
// the downstream handler with cluster.Name set to that name.
func TestWithLocalProxy_BareNameIsForwarded(t *testing.T) {
	var gotName string
	downstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if cluster := request.ClusterFrom(req.Context()); cluster != nil {
			gotName = cluster.Name.String()
		}
		w.WriteHeader(http.StatusOK)
	})

	h, err := WithLocalProxy(downstream, "test-shard", "", index.New(nil))
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet,
		"/clusters/25t3xbr5iceb0155/apis/networking.dev/v1/namespaces/proj-x/ipallocations/foo",
		http.NoBody)
	rr := httptest.NewRecorder()

	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "bare cluster name must be forwarded, got %d", rr.Code)
	require.Equal(t, "25t3xbr5iceb0155", gotName, "downstream must see the bare cluster name on context")
}
