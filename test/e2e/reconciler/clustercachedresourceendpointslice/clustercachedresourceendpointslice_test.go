/*
Copyright 2025 The kcp Authors.

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

package clustercachedresourceendpointslice

import (
	"fmt"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestClusterCachedResourceEndpointSliceWithPath(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx := t.Context()
	server := kcptesting.SharedKcpServer(t)

	// Create Organization and Workspaces
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"))
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer"))

	cfg := server.BaseConfig(t)

	var err error
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	// Create a CRD in the provider workspace using the wildwest fixture
	t.Logf("Creating wildwest.dev CRD in provider workspace %q", providerPath)
	kcpApiExtensionClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp apiextensions cluster client")
	crdClient := kcpApiExtensionClusterClient.ApiextensionsV1().CustomResourceDefinitions()
	sheriffsGR := metav1.GroupResource{Group: "wildwest.dev", Resource: "sheriffs"}
	wildwest.Create(t, providerPath, crdClient, sheriffsGR)

	// Create a ClusterCachedResource in the provider workspace
	t.Logf("Creating ClusterCachedResource in provider workspace %q", providerPath)
	clusterCachedResource := &cachev1alpha1.ClusterCachedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: sheriffsGR.String(),
		},
		Spec: cachev1alpha1.ClusterCachedResourceSpec{
			GroupVersionResource: cachev1alpha1.GroupVersionResource{
				Group:    "wildwest.dev",
				Version:  "v1alpha1",
				Resource: "sheriffs",
			},
		},
	}

	clusterCachedResourceClient := kcpClusterClient.CacheV1alpha1().ClusterCachedResources()
	_, err = clusterCachedResourceClient.Cluster(providerPath).Create(ctx, clusterCachedResource, metav1.CreateOptions{})
	require.NoError(t, err, "error creating ClusterCachedResource")

	// Wait for ClusterCachedResource to be ready
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		clusterCachedResource, err = clusterCachedResourceClient.Cluster(providerPath).Get(ctx, clusterCachedResource.Name, metav1.GetOptions{})
		return clusterCachedResource, err
	}, kcptestinghelpers.Is(cachev1alpha1.ReplicationStarted), fmt.Sprintf("ClusterCachedResource %v should become ready", clusterCachedResource.Name))

	// Create a ClusterCachedResourceEndpointSlice in the consumer workspace that references
	// the ClusterCachedResource in the provider workspace using the path field
	t.Logf("Creating ClusterCachedResourceEndpointSlice in consumer workspace %q with path reference to provider workspace %q", consumerPath, providerPath)
	sliceExternalPath := &cachev1alpha1.ClusterCachedResourceEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "sheriffs-external-path-slice-",
		},
		Spec: cachev1alpha1.ClusterCachedResourceEndpointSliceSpec{
			ClusterCachedResource: cachev1alpha1.ClusterCachedResourceReference{
				Path: providerPath.String(),
				Name: clusterCachedResource.Name,
			},
		},
	}

	sliceClient := kcpClusterClient.CacheV1alpha1().ClusterCachedResourceEndpointSlices()

	var sliceExternalPathName string
	t.Logf("Creating ClusterCachedResourceEndpointSlice with external path reference")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		created, err := sliceClient.Cluster(consumerPath).Create(ctx, sliceExternalPath, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		sliceExternalPathName = created.Name
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected ClusterCachedResourceEndpointSlice creation to succeed")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		sliceExternalPath, err = kcpClusterClient.Cluster(consumerPath).CacheV1alpha1().ClusterCachedResourceEndpointSlices().Get(ctx, sliceExternalPathName, metav1.GetOptions{})
		require.NoError(t, err)

		if conditions.IsTrue(sliceExternalPath, cachev1alpha1.ClusterCachedResourceValid) {
			return true, ""
		}

		return false, spew.Sdump(sliceExternalPath.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected valid ClusterCachedResource reference")

	t.Logf("Creating ClusterCachedResourceEndpointSlice in provider workspace that references the ClusterCachedResource in the same workspace")
	sliceSameWorkspace := &cachev1alpha1.ClusterCachedResourceEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "sheriffs-same-workspace-slice-",
		},
		Spec: cachev1alpha1.ClusterCachedResourceEndpointSliceSpec{
			ClusterCachedResource: cachev1alpha1.ClusterCachedResourceReference{
				Name: clusterCachedResource.Name,
			},
		},
	}

	var sliceSameWorkspaceName string
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		created, err := sliceClient.Cluster(providerPath).Create(ctx, sliceSameWorkspace, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		sliceSameWorkspaceName = created.Name
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected ClusterCachedResourceEndpointSlice creation in same workspace to succeed")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		sliceSameWorkspace, err = kcpClusterClient.Cluster(providerPath).CacheV1alpha1().ClusterCachedResourceEndpointSlices().Get(ctx, sliceSameWorkspaceName, metav1.GetOptions{})
		require.NoError(t, err)

		if conditions.IsTrue(sliceSameWorkspace, cachev1alpha1.ClusterCachedResourceValid) {
			return true, ""
		}

		return false, spew.Sdump(sliceSameWorkspace.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected valid ClusterCachedResource reference in same workspace")

	t.Logf("ClusterCachedResourceEndpointSlice successfully references ClusterCachedResource in same workspace")

	sliceInvalidReference := &cachev1alpha1.ClusterCachedResourceEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sheriffs-invalid-reference-slice",
		},
		Spec: cachev1alpha1.ClusterCachedResourceEndpointSliceSpec{
			ClusterCachedResource: cachev1alpha1.ClusterCachedResourceReference{
				Path: providerPath.String(),
				Name: "nonexistent-clustercachedresource",
			},
		},
	}

	_, err = sliceClient.Cluster(consumerPath).Create(ctx, sliceInvalidReference, metav1.CreateOptions{})
	require.NoError(t, err, "ClusterCachedResourceEndpointSlice should be created even if ClusterCachedResource doesn't exist yet")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		sliceInvalidReference, err = kcpClusterClient.Cluster(consumerPath).CacheV1alpha1().ClusterCachedResourceEndpointSlices().Get(ctx, sliceInvalidReference.Name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		if conditions.IsFalse(sliceInvalidReference, cachev1alpha1.ClusterCachedResourceValid) &&
			conditions.GetReason(sliceInvalidReference, cachev1alpha1.ClusterCachedResourceValid) == cachev1alpha1.ClusterCachedResourceNotFoundReason {
			return true, ""
		}
		return false, spew.Sdump(sliceInvalidReference.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected invalid ClusterCachedResource reference")

	t.Logf("ClusterCachedResourceEndpointSlice correctly reports invalid reference to nonexistent ClusterCachedResource")
}
