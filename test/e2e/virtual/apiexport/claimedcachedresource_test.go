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

package apiexport

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestClaimedResourceFromClusterCachedResource covers a permission claim on a
// resource the producing APIExport serves from a ClusterCachedResource rather
// than from CRD storage.
//
// A claim on such a resource is admitted, bound and advertised, and then reads
// empty. The claimed read is filtered by the claim's label requirement
// (apiexport_apireconciler_reconcile.go builds it, builder/build.go wraps the
// storage in it), and the permission-claim label controller only labels objects
// in the consumer workspace. Replicated objects are never labelled, so the
// requirement excludes every one of them and the caller is handed an empty list
// with no error.
//
// The failure mode is what makes this worth a test: a claimer cannot tell "this
// workspace holds none of these" from "this kind cannot be claimed", so it
// converges on the empty answer instead of failing.
func TestClaimedResourceFromClusterCachedResource(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClients, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	apiExtensionClients, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions cluster client for server")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	claimerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	const (
		producerExport       = "cached-sheriffs"
		matchAllClaimer      = "sheriff-reader"
		matchLabelsClaimer   = "sheriff-label-reader"
		sourceSheriff        = "woody"
		endpointSliceAndCRes = "sheriffs.wildwest.dev"
	)

	sheriffsGR := metav1.GroupResource{Group: wildwestv1alpha1.SchemeGroupVersion.Group, Resource: "sheriffs"}
	sheriffsGVR := wildwestv1alpha1.SchemeGroupVersion.WithResource("sheriffs")
	consumerCluster := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	//
	// Producer: a Sheriff the cache replicates, exported through virtual storage.
	//

	t.Logf("Create the sheriffs CRD and its APIResourceSchema in %q", providerPath)
	wildwest.Create(t, providerPath, apiExtensionClients.ApiextensionsV1().CustomResourceDefinitions(), sheriffsGR)
	kcptesting.WaitForAPIReady(t, kcpClients.Cluster(providerPath).Discovery(), wildwestv1alpha1.SchemeGroupVersion)

	schemaForCRD, err := apisv1alpha1.CRDToAPIResourceSchema(wildwest.CRD(t, sheriffsGR), "today")
	require.NoError(t, err)
	_, err = kcpClients.Cluster(providerPath).ApisV1alpha1().APIResourceSchemas().Create(t.Context(), schemaForCRD, metav1.CreateOptions{})
	require.NoError(t, err)

	// The source object lives in the producer workspace. Every consumer is served
	// the cache's single copy of it, which is the whole point of the arrangement
	// and the reason there is no per-consumer object to label.
	t.Logf("Create the source Sheriff %q in %q", sourceSheriff, providerPath)
	createSheriff(t.Context(), t, dynamicClusterClient, providerPath, sheriffsGR.Group, sourceSheriff)

	t.Logf("Create the ClusterCachedResource for %s in %q", sheriffsGR, providerPath)
	clusterCachedResource, err := kcpClients.Cluster(providerPath).CacheV1alpha1().ClusterCachedResources().Create(t.Context(), &cachev1alpha1.ClusterCachedResource{
		ObjectMeta: metav1.ObjectMeta{Name: endpointSliceAndCRes},
		Spec: cachev1alpha1.ClusterCachedResourceSpec{
			GroupVersionResource: cachev1alpha1.GroupVersionResource{
				Group:    sheriffsGVR.Group,
				Version:  sheriffsGVR.Version,
				Resource: sheriffsGVR.Resource,
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClients.Cluster(providerPath).CacheV1alpha1().ClusterCachedResources().Get(t.Context(), clusterCachedResource.Name, metav1.GetOptions{})
	}, kcptestinghelpers.Is(cachev1alpha1.ReplicationStarted), "ClusterCachedResource should start replicating")

	t.Logf("Create the producer APIExport %q, serving sheriffs from the cache", producerExport)
	_, err = kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: producerExport},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{{
				Group:  sheriffsGR.Group,
				Name:   sheriffsGR.Resource,
				Schema: "today.sheriffs.wildwest.dev",
				Storage: apisv1alpha2.ResourceSchemaStorage{
					Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
						Reference: corev1.TypedLocalObjectReference{
							APIGroup: ptr.To(cachev1alpha1.SchemeGroupVersion.Group),
							Kind:     "ClusterCachedResourceEndpointSlice",
							Name:     endpointSliceAndCRes,
						},
					},
				},
			}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create the ClusterCachedResourceEndpointSlice %q in %q", endpointSliceAndCRes, providerPath)
	endpointSlice, err := kcpClients.Cluster(providerPath).CacheV1alpha1().ClusterCachedResourceEndpointSlices().Create(t.Context(), &cachev1alpha1.ClusterCachedResourceEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: endpointSliceAndCRes},
		Spec: cachev1alpha1.ClusterCachedResourceEndpointSliceSpec{
			ClusterCachedResource: cachev1alpha1.ClusterCachedResourceReference{Name: endpointSliceAndCRes},
			APIExport:             cachev1alpha1.ExportBindingReference{Name: producerExport},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	getEndpointSlice := func() (conditions.Getter, error) {
		return kcpClients.Cluster(providerPath).CacheV1alpha1().ClusterCachedResourceEndpointSlices().Get(t.Context(), endpointSlice.Name, metav1.GetOptions{})
	}
	kcptestinghelpers.EventuallyCondition(t, getEndpointSlice, kcptestinghelpers.Is(cachev1alpha1.ClusterCachedResourceValid),
		"ClusterCachedResourceEndpointSlice should resolve its ClusterCachedResource")
	kcptestinghelpers.EventuallyCondition(t, getEndpointSlice, kcptestinghelpers.Is(cachev1alpha1.APIExportValid),
		"ClusterCachedResourceEndpointSlice should resolve its APIExport")

	t.Logf("Get the producer APIExport identity hash")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), producerExport, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))
	export, err := kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), producerExport, metav1.GetOptions{})
	require.NoError(t, err)
	identityHash := export.Status.IdentityHash

	//
	// Consumer: binds the producer, so the replicated Sheriff is served here.
	//

	t.Logf("Bind the producer APIExport in %q", consumerPath)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: producerExport},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{Path: providerPath.String(), Name: producerExport},
				},
			},
		}, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	// The control. Without this the test could pass or fail for fixture reasons
	// rather than because of the claim, so assert the ordinary route first: the
	// consumer's own binding serves the replicated Sheriff.
	t.Logf("Wait for the replicated Sheriff to be served in %q through the consumer's own binding", consumerPath)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		list, err := dynamicClusterClient.Cluster(consumerPath).Resource(sheriffsGVR).List(t.Context(), metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error listing sheriffs in the consumer: %v", err)
		}
		return len(list.Items) == 1, fmt.Sprintf("consumer sees %d sheriffs, want 1", len(list.Items))
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	//
	// Claimer: claims the cached resource and reads it through its own VW.
	//

	createClaimer := func(name string, selector apisv1alpha2.PermissionClaimSelector) error {
		t.Helper()

		claim := apisv1alpha2.PermissionClaim{
			GroupResource: apisv1alpha2.GroupResource{Group: sheriffsGR.Group, Resource: sheriffsGR.Resource},
			Verbs:         []string{"get", "list", "watch"},
			IdentityHash:  identityHash,
		}

		t.Logf("Create claimer APIExport %q", name)
		if _, err := kcpClients.Cluster(claimerPath).ApisV1alpha2().APIExports().Create(t.Context(), &apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       apisv1alpha2.APIExportSpec{PermissionClaims: []apisv1alpha2.PermissionClaim{claim}},
		}, metav1.CreateOptions{}); err != nil {
			return err
		}

		t.Logf("Bind claimer APIExport %q in %q, accepting its claim", name, consumerPath)
		var bindErr error
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, bindErr = kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), &apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{Path: claimerPath.String(), Name: name},
					},
					PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{{
						State: apisv1alpha2.ClaimAccepted,
						ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
							PermissionClaim: claim,
							Selector:        selector,
						},
					}},
				},
			}, metav1.CreateOptions{})
			// An invalid claim is the answer this test wants for the matchLabels
			// case, so stop retrying on anything that is not transient.
			return bindErr == nil || !isRetryableBindError(bindErr), fmt.Sprintf("error creating APIBinding: %v", bindErr)
		}, wait.ForeverTestTimeout, time.Millisecond*100)
		return bindErr
	}

	t.Run("a matchAll claim on a cached resource serves the producer's objects", func(t *testing.T) {
		require.NoError(t, createClaimer(matchAllClaimer, apisv1alpha2.PermissionClaimSelector{MatchAll: true}))

		vwDynamic, err := kcpdynamic.NewForConfig(vwConfig(t, cfg, kcpClients, consumerWorkspace, claimerPath, matchAllClaimer))
		require.NoError(t, err)

		// The resource is advertised first; the defect is in what it serves, not
		// in whether it is there. Asserting discovery separately keeps a genuine
		// regression (no API at all) from being reported as an empty list.
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			list, err := kcpClients.Cluster(consumerCluster.Path()).Discovery().ServerResourcesForGroupVersion(sheriffsGVR.GroupVersion().String())
			if err != nil {
				return false, fmt.Sprintf("error getting discovery: %v", err)
			}
			for _, resource := range list.APIResources {
				if resource.Name == sheriffsGVR.Resource {
					return true, ""
				}
			}
			return false, fmt.Sprintf("sheriffs not advertised, have %v", resourceNames(list))
		}, wait.ForeverTestTimeout, time.Millisecond*100)

		kcptestinghelpers.Eventually(t, func() (bool, string) {
			list, err := vwDynamic.Cluster(consumerCluster.Path()).Resource(sheriffsGVR).List(t.Context(), metav1.ListOptions{})
			if err != nil {
				return false, fmt.Sprintf("error listing sheriffs through the claimer's VW: %v", err)
			}
			// Today this is 0: the claim's label requirement excludes every
			// replicated object, because nothing labels them.
			return len(list.Items) == 1, fmt.Sprintf("claimer's VW serves %d sheriffs, want 1", len(list.Items))
		}, wait.ForeverTestTimeout, time.Millisecond*100)
	})

	t.Run("a matchLabels claim on a cached resource is refused", func(t *testing.T) {
		// A per-object label selector cannot be honoured over a single shared
		// read-only copy, and silently serving matchAll semantics instead would be
		// worse than serving nothing. Refusing it is the only answer that leaves
		// the claimer able to tell what happened.
		err := createClaimer(matchLabelsClaimer, apisv1alpha2.PermissionClaimSelector{
			LabelSelector: metav1.LabelSelector{MatchLabels: map[string]string{"wildwest.dev/tier": "sheriff"}},
		})
		require.Error(t, err, "a matchLabels claim on a resource served from a ClusterCachedResource should be refused")
	})
}

// isRetryableBindError reports whether creating an APIBinding failed for a
// reason worth retrying. A validation failure is an answer, not a flake.
func isRetryableBindError(err error) bool {
	return !apierrors.IsInvalid(err) && !apierrors.IsBadRequest(err) && !apierrors.IsForbidden(err)
}
