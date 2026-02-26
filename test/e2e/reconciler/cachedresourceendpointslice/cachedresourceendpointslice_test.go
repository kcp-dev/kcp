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

package cachedresourceendpointslice

import (
	"context"
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

func TestCachedResourceEndpointSliceWithPath(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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

	// Create a CachedResource in the provider workspace
	t.Logf("Creating CachedResource in provider workspace %q", providerPath)
	cachedResource := &cachev1alpha1.CachedResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: sheriffsGR.String(),
		},
		Spec: cachev1alpha1.CachedResourceSpec{
			GroupVersionResource: cachev1alpha1.GroupVersionResource{
				Group:    "wildwest.dev",
				Version:  "v1alpha1",
				Resource: "sheriffs",
			},
		},
	}

	cachedResourceClient := kcpClusterClient.CacheV1alpha1().CachedResources()
	_, err = cachedResourceClient.Cluster(providerPath).Create(ctx, cachedResource, metav1.CreateOptions{})
	require.NoError(t, err, "error creating CachedResource")

	// Wait for CachedResource to be ready
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		cachedResource, err = cachedResourceClient.Cluster(providerPath).Get(ctx, cachedResource.Name, metav1.GetOptions{})
		return cachedResource, err
	}, kcptestinghelpers.Is(cachev1alpha1.ReplicationStarted), fmt.Sprintf("CachedResource %v should become ready", cachedResource.Name))

	// Create a CachedResourceEndpointSlice in the consumer workspace that references
	// the CachedResource in the provider workspace using the path field
	t.Logf("Creating CachedResourceEndpointSlice in consumer workspace %q with path reference to provider workspace %q", consumerPath, providerPath)
	sliceExternalPath := &cachev1alpha1.CachedResourceEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "sheriffs-external-path-slice-",
		},
		Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
			CachedResource: cachev1alpha1.CachedResourceReference{
				Path: providerPath.String(),
				Name: cachedResource.Name,
			},
		},
	}

	sliceClient := kcpClusterClient.CacheV1alpha1().CachedResourceEndpointSlices()

	var sliceExternalPathName string
	t.Logf("Creating CachedResourceEndpointSlice with external path reference")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		created, err := sliceClient.Cluster(consumerPath).Create(ctx, sliceExternalPath, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		sliceExternalPathName = created.Name
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected CachedResourceEndpointSlice creation to succeed")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		sliceExternalPath, err = kcpClusterClient.Cluster(consumerPath).CacheV1alpha1().CachedResourceEndpointSlices().Get(ctx, sliceExternalPathName, metav1.GetOptions{})
		require.NoError(t, err)

		if conditions.IsTrue(sliceExternalPath, cachev1alpha1.CachedResourceValid) {
			return true, ""
		}

		return false, spew.Sdump(sliceExternalPath.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected valid CachedResource reference")

	t.Logf("Creating CachedResourceEndpointSlice in provider workspace that references the CachedResource in the same workspace")
	sliceSameWorkspace := &cachev1alpha1.CachedResourceEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "sheriffs-same-workspace-slice-",
		},
		Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
			CachedResource: cachev1alpha1.CachedResourceReference{
				Name: cachedResource.Name,
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
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected CachedResourceEndpointSlice creation in same workspace to succeed")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		sliceSameWorkspace, err = kcpClusterClient.Cluster(providerPath).CacheV1alpha1().CachedResourceEndpointSlices().Get(ctx, sliceSameWorkspaceName, metav1.GetOptions{})
		require.NoError(t, err)

		if conditions.IsTrue(sliceSameWorkspace, cachev1alpha1.CachedResourceValid) {
			return true, ""
		}

		return false, spew.Sdump(sliceSameWorkspace.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected valid CachedResource reference in same workspace")

	t.Logf("CachedResourceEndpointSlice successfully references CachedResource in same workspace")

	sliceInvalidReference := &cachev1alpha1.CachedResourceEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sheriffs-invalid-reference-slice",
		},
		Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
			CachedResource: cachev1alpha1.CachedResourceReference{
				Path: providerPath.String(),
				Name: "nonexistent-cachedresource",
			},
		},
	}

	_, err = sliceClient.Cluster(consumerPath).Create(ctx, sliceInvalidReference, metav1.CreateOptions{})
	require.NoError(t, err, "CachedResourceEndpointSlice should be created even if CachedResource doesn't exist yet")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		sliceInvalidReference, err = kcpClusterClient.Cluster(consumerPath).CacheV1alpha1().CachedResourceEndpointSlices().Get(ctx, sliceInvalidReference.Name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		if conditions.IsFalse(sliceInvalidReference, cachev1alpha1.CachedResourceValid) &&
			conditions.GetReason(sliceInvalidReference, cachev1alpha1.CachedResourceValid) == cachev1alpha1.CachedResourceNotFoundReason {
			return true, ""
		}
		return false, spew.Sdump(sliceInvalidReference.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected invalid CachedResource reference")

	t.Logf("CachedResourceEndpointSlice correctly reports invalid reference to nonexistent CachedResource")
}
