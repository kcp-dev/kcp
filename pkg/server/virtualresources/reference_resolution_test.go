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

package virtualresources

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	corev1 "k8s.io/api/core/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/reconciler/dynamicrestmapper"
)

const (
	providerCluster = logicalcluster.Name("provider-cluster")
	exportName      = "edges.example.com"
)

func cachedResource(name, referencedBy, referencedKind, group, version, resource string, names ...string) *cachev1alpha1.ClusterCachedResource {
	return &cachev1alpha1.ClusterCachedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				cachev1alpha1.ReferencedByAnnotationKey:   referencedBy,
				cachev1alpha1.ReferencedKindAnnotationKey: referencedKind,
			},
		},
		Spec: cachev1alpha1.ClusterCachedResourceSpec{
			GroupVersionResource: cachev1alpha1.GroupVersionResource{
				Group:    group,
				Version:  version,
				Resource: resource,
			},
			Names: names,
		},
	}
}

func serverWithCachedResources(crs ...*cachev1alpha1.ClusterCachedResource) *Server {
	return &Server{
		// Deliberately empty: it stands for the shard the APIExport is NOT on,
		// which is the whole point. Any resolution that reaches it fails.
		drm: dynamicrestmapper.NewDynamicRESTMapper(),
		listClusterCachedResources: func(logicalcluster.Name) ([]*cachev1alpha1.ClusterCachedResource, error) {
			return crs, nil
		},
	}
}

func reference(group, kind, name string) corev1.TypedLocalObjectReference {
	return corev1.TypedLocalObjectReference{APIGroup: ptr.To(group), Kind: kind, Name: name}
}

// The APIExport's own shard resolved the reference and recorded the result, so a
// shard that does not serve that logical cluster can still route the request.
// Before this, the RESTMapper lookup below was the only path and every custom
// subresource of a cross-shard binding failed with "no matches for kind".
func TestResolveReferenceGVRFromCachedResourceWithoutARESTMapping(t *testing.T) {
	t.Parallel()

	s := serverWithCachedResources(cachedResource(
		"apiexport-dataplaneendpointslices-abc", exportName, "DataPlaneEndpointSlice",
		"dataplane.example.com", "v1alpha1", "dataplaneendpointslices", exportName,
	))

	gvr, err := s.resolveReferenceGVR(providerCluster, exportName,
		reference("dataplane.example.com", "DataPlaneEndpointSlice", exportName))
	require.NoError(t, err, "the reference must resolve without a RESTMapping for a foreign cluster")
	require.Equal(t, schema.GroupVersionResource{
		Group:    "dataplane.example.com",
		Version:  "v1alpha1",
		Resource: "dataplaneendpointslices",
	}, gvr)
}

// Matching is on all of export, kind, group and name: a near miss must not
// silently route a request to the wrong resource, it must fall through.
func TestResolveReferenceGVRIgnoresCachedResourcesThatDoNotMatch(t *testing.T) {
	t.Parallel()

	match := reference("dataplane.example.com", "DataPlaneEndpointSlice", exportName)
	for name, cr := range map[string]*cachev1alpha1.ClusterCachedResource{
		"another APIExport's reference": cachedResource("a", "other.example.com", "DataPlaneEndpointSlice",
			"dataplane.example.com", "v1alpha1", "dataplaneendpointslices", exportName),
		"another kind in the same group": cachedResource("b", exportName, "SomethingElse",
			"dataplane.example.com", "v1alpha1", "somethingelses", exportName),
		"another group": cachedResource("c", exportName, "DataPlaneEndpointSlice",
			"other.example.com", "v1alpha1", "dataplaneendpointslices", exportName),
		"another object of the right kind": cachedResource("d", exportName, "DataPlaneEndpointSlice",
			"dataplane.example.com", "v1alpha1", "dataplaneendpointslices", "a-different-slice"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := serverWithCachedResources(cr).resolveReferenceGVR(providerCluster, exportName, match)
			require.Error(t, err, "a non-matching cached resource must not be used")
			require.True(t, meta.IsNoMatchError(err), "want the RESTMapper's no-match error, got %v", err)
		})
	}
}

// With nothing recorded yet, the RESTMapper is still consulted, so a reference
// to a type this shard does know about keeps working.
func TestResolveReferenceGVRFallsBackToTheRESTMapper(t *testing.T) {
	t.Parallel()

	s := serverWithCachedResources()
	_, err := s.resolveReferenceGVR(providerCluster, exportName,
		reference("dataplane.example.com", "DataPlaneEndpointSlice", exportName))
	require.Error(t, err)
	require.True(t, meta.IsNoMatchError(err), "want the RESTMapper's no-match error, got %v", err)
}

// A listing failure is reported rather than quietly treated as "nothing cached",
// which would turn a transient informer problem into a wrong answer.
func TestResolveReferenceGVRPropagatesListErrors(t *testing.T) {
	t.Parallel()

	boom := errors.New("informer not synced")
	s := &Server{
		drm: dynamicrestmapper.NewDynamicRESTMapper(),
		listClusterCachedResources: func(logicalcluster.Name) ([]*cachev1alpha1.ClusterCachedResource, error) {
			return nil, boom
		},
	}
	_, err := s.resolveReferenceGVR(providerCluster, exportName,
		reference("dataplane.example.com", "DataPlaneEndpointSlice", exportName))
	require.ErrorIs(t, err, boom)
}
