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

package builder

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry"

	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/controllers/apireconciler"
)

// TestCustomSubresourceConnectMethods covers the translation from the verbs a
// subresource is served under to the HTTP methods it accepts, which is what
// discovery reports and what the handler enforces.
func TestCustomSubresourceConnectMethods(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		verbs []string
		want  []string
	}{
		{
			name:  "a create-only claim accepts POST",
			verbs: []string{"create"},
			want:  []string{http.MethodPost},
		},
		{
			name:  "methods are sorted, so discovery does not change between identical builds",
			verbs: []string{"update", "get", "create"},
			want:  []string{http.MethodGet, http.MethodPost, http.MethodPut},
		},
		{
			name:  "a wildcard claim accepts every method",
			verbs: []string{"*"},
			want:  []string{http.MethodDelete, http.MethodGet, http.MethodPatch, http.MethodPost, http.MethodPut},
		},
		{
			name:  "verbs a subresource cannot carry contribute no method",
			verbs: []string{"list", "watch", "deletecollection"},
			want:  []string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := newCustomSubresourceStorage(
				schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "cowboys"},
				apireconciler.CustomSubresource{Name: "shoot", Verbs: tc.verbs},
				false, nil, nil, nil, nil,
			)
			require.Equal(t, tc.want, s.ConnectMethods())
		})
	}
}

// TestCustomSubresourceTargetPath covers where on the shard a request for a
// custom subresource is sent. The path is what puts the request back under the
// resource the subresource belongs to, so that the shard resolves the entry and
// forwards it to the provider's endpoint.
func TestCustomSubresourceTargetPath(t *testing.T) {
	t.Parallel()

	cowboys := schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "cowboys"}
	cluster := logicalcluster.Name("consumer")

	storage := func(gvr schema.GroupVersionResource, namespaceScoped bool, identities forwardingregistry.IdentityHashesFunc) *customSubresourceStorage {
		return newCustomSubresourceStorage(
			gvr,
			apireconciler.CustomSubresource{Name: "shoot", Verbs: []string{"create"}},
			namespaceScoped, identities, nil, nil, nil,
		)
	}

	t.Run("cluster-scoped", func(t *testing.T) {
		t.Parallel()
		s := storage(cowboys, false, nil)
		require.Equal(t,
			"/clusters/consumer/apis/wildwest.dev/v1alpha1/cowboys/lucky-luke/shoot",
			s.targetPath(context.Background(), cluster, "lucky-luke"))
	})

	t.Run("namespaced", func(t *testing.T) {
		t.Parallel()
		s := storage(cowboys, true, nil)
		ctx := genericapirequest.WithNamespace(context.Background(), "saloon")
		require.Equal(t,
			"/clusters/consumer/apis/wildwest.dev/v1alpha1/namespaces/saloon/cowboys/lucky-luke/shoot",
			s.targetPath(ctx, cluster, "lucky-luke"))
	})

	t.Run("the core group is served under /api", func(t *testing.T) {
		t.Parallel()
		s := storage(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, true, nil)
		ctx := genericapirequest.WithNamespace(context.Background(), "saloon")
		require.Equal(t,
			"/clusters/consumer/api/v1/namespaces/saloon/configmaps/settings/shoot",
			s.targetPath(ctx, cluster, "settings"))
	})

	t.Run("a single identity is named, the way the forwarding storage names it", func(t *testing.T) {
		t.Parallel()
		s := storage(cowboys, false, func(context.Context) []string { return []string{"abcdef"} })
		require.Equal(t,
			"/clusters/consumer/apis/wildwest.dev/v1alpha1/cowboys:abcdef/lucky-luke/shoot",
			s.targetPath(context.Background(), cluster, "lucky-luke"))
	})

	t.Run("several identities leave the resolution to the shard", func(t *testing.T) {
		t.Parallel()
		s := storage(cowboys, false, func(context.Context) []string { return []string{"abcdef", "123456"} })
		require.Equal(t,
			"/clusters/consumer/apis/wildwest.dev/v1alpha1/cowboys/lucky-luke/shoot",
			s.targetPath(context.Background(), cluster, "lucky-luke"),
			"no identity is named, so the shard resolves it from the APIBinding")
	})
}

// TestCustomSubresourceGatesOnParent covers that a subresource is only reachable
// on an object the parent storage would serve.
//
// A claim may carry a selector, and the parent storage is where that selector is
// applied. Reaching the subresource without consulting it would let a claimer act
// on an object its own claim excludes.
func TestCustomSubresourceGatesOnParent(t *testing.T) {
	t.Parallel()

	newStorage := func(parentGet func(context.Context, string, *metav1.GetOptions) (runtime.Object, error)) *customSubresourceStorage {
		return newCustomSubresourceStorage(
			schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "cowboys"},
			apireconciler.CustomSubresource{Name: "shoot", Verbs: []string{"create"}},
			false, nil,
			func(logicalcluster.Name) (string, error) { return "", nil },
			parentGet,
			&shardProxy{host: &url.URL{Scheme: "https", Host: "shard.example.com"}},
		)
	}

	ctx := genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Name: "consumer"})
	ctx = genericapirequest.WithUser(ctx, &user.DefaultInfo{Name: "someone"})

	t.Run("the parent's error is the answer", func(t *testing.T) {
		t.Parallel()

		// A selector that excludes the object makes the parent read as not found,
		// and the subresource must read the same way rather than reporting that
		// the object exists.
		wanted := apierrors.NewNotFound(schema.GroupResource{Group: "wildwest.dev", Resource: "cowboys"}, "lucky-luke")
		s := newStorage(func(context.Context, string, *metav1.GetOptions) (runtime.Object, error) {
			return nil, wanted
		})

		_, err := s.Connect(ctx, "lucky-luke", nil, nil)
		require.Equal(t, wanted, err)
	})

	t.Run("a served parent lets the request through", func(t *testing.T) {
		t.Parallel()

		var asked string
		s := newStorage(func(_ context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
			asked = name
			return &unstructured.Unstructured{}, nil
		})

		handler, err := s.Connect(ctx, "lucky-luke", nil, nil)
		require.NoError(t, err)
		require.NotNil(t, handler)
		require.Equal(t, "lucky-luke", asked, "the gate must read the object the request names")
	})

	t.Run("no parent storage is an error, never an open door", func(t *testing.T) {
		t.Parallel()

		_, err := newStorage(nil).Connect(ctx, "lucky-luke", nil, nil)
		require.True(t, apierrors.IsInternalError(err), "got: %v", err)
	})
}
