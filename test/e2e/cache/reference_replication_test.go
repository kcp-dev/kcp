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

package cache

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	cache2e "github.com/kcp-dev/kcp/test/e2e/reconciler/cache"
)

// TestReferenceReplication covers an object reaching the cache because an
// APIExport points at it through spec.resources[].storage.virtual.reference,
// and leaving again when nothing does.
//
// The kind matters. Sheriffs are not something the replication controller
// copies on its own, so whether a sheriff is in the cache is decided entirely
// by whether an APIExport references it -- which is the whole point of the
// feature. Pick a kind that is replicated unconditionally (an endpoint slice,
// say) and this test would pass without the reference machinery doing anything.
func TestReferenceReplication(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "error creating dynamic cluster client")

	apiExtensionsClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating apiextensions cluster client")

	cacheClientCfg := createCacheClientConfigForEnvironment(t, server.RootShardSystemMasterBaseConfig(t))
	cacheDynClient, err := kcpdynamic.NewForConfig(cache2e.ClientRoundTrippersFor(cacheClientCfg))
	require.NoError(t, err, "error creating cache dynamic client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())
	providerPath, providerWS := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	// The cache server keys by logical cluster name and knows nothing of the
	// workspace hierarchy, so reads against it cannot go through the path.
	providerCluster := logicalcluster.Name(providerWS.Spec.Cluster)

	gr := metav1.GroupResource{Group: "wildwest.dev", Resource: "sheriffs"}
	sheriffsGVR := wildwestv1alpha1.SchemeGroupVersion.WithResource("sheriffs")

	t.Logf("Creating the sheriffs CRD in %q", providerPath)
	wildwest.Create(t, providerPath, apiExtensionsClient.ApiextensionsV1().CustomResourceDefinitions(), gr)
	kcptesting.WaitForAPIReady(t, kcpClusterClient.Cluster(providerPath).Discovery(), wildwestv1alpha1.SchemeGroupVersion)

	const (
		referenced   = "referenced-sheriff"
		unreferenced = "unreferenced-sheriff"
		exportName   = "wildwest-provider"
	)

	for _, name := range []string{referenced, unreferenced} {
		t.Logf("Creating Sheriff %q in %q", name, providerPath)
		_, err = dynamicClusterClient.Cluster(providerPath).Resource(sheriffsGVR).Create(ctx, &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": wildwestv1alpha1.SchemeGroupVersion.String(),
				"kind":       "Sheriff",
				"metadata":   map[string]interface{}{"name": name},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err, "error creating Sheriff %s", name)
	}

	exports := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports()

	t.Logf("Creating APIExport %q referencing Sheriff %q", exportName, referenced)
	_, err = exports.Create(ctx, &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: exportName},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{{
				Group:  "wildwest.dev",
				Name:   "cowboys",
				Schema: "today.cowboys.wildwest.dev",
				Storage: apisv1alpha2.ResourceSchemaStorage{
					Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
						Reference: corev1.TypedLocalObjectReference{
							APIGroup: ptr.To("wildwest.dev"),
							Kind:     "Sheriff",
							Name:     referenced,
						},
					},
				},
			}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExport")

	cachedSheriffs := func() ([]string, error) {
		list, err := cacheDynClient.Cluster(providerCluster.Path()).Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(list.Items))
		for _, item := range list.Items {
			names = append(names, item.GetName())
		}
		return names, nil
	}

	t.Logf("Waiting for the referenced Sheriff to reach the cache server")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		names, err := cachedSheriffs()
		if err != nil {
			return false, fmt.Sprintf("error listing sheriffs in the cache: %v", err)
		}
		return slices.Contains(names, referenced), fmt.Sprintf("cache holds %v", names)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "the referenced Sheriff never reached the cache")

	// Replicating what is referenced rather than what exists is the difference
	// between this and the replication controller, so it is worth asserting
	// that the sheriff nobody named stayed out.
	names, err := cachedSheriffs()
	require.NoError(t, err)
	require.NotContains(t, names, unreferenced,
		"a Sheriff no APIExport references must not be replicated")

	t.Logf("Deleting APIExport %q", exportName)
	require.NoError(t, exports.Delete(ctx, exportName, metav1.DeleteOptions{}), "error deleting APIExport")

	t.Logf("Waiting for the cached copy to be removed")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		names, err := cachedSheriffs()
		if apierrors.IsNotFound(err) {
			// The kind is served out of a synthetic CRD that exists only as
			// long as some ClusterCachedResource wants it. With the last one
			// gone the whole kind goes, which removes the copy and then some.
			return true, ""
		}
		if err != nil {
			return false, fmt.Sprintf("error listing sheriffs in the cache: %v", err)
		}
		return !slices.Contains(names, referenced), fmt.Sprintf("cache holds %v", names)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "the cached copy outlived the last reference to it")

	// The local object is untouched: only the copy was ever this controller's
	// to remove.
	_, err = dynamicClusterClient.Cluster(providerPath).Resource(sheriffsGVR).Get(ctx, referenced, metav1.GetOptions{})
	require.NoError(t, err, "replication must not delete the object it was copying")
}
