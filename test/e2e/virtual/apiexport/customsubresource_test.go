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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
	"k8s.io/utils/ptr"

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

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// unresolvableEndpointSlice names an endpoint slice that is deliberately never
// created.
//
// Resolving it is the first thing the shard does once it has decided a request
// belongs to a virtual resource. Its absence turns "the request reached the
// virtual-resource path on the shard" into an observable error, without needing a
// virtual workspace that actually serves the subresource -- which lives out of
// tree.
const unresolvableEndpointSlice = "no-such-endpointslice"

// TestCustomSubresourceClaimsThroughVW covers a service provider reaching another
// provider's custom subresource through its own APIExport virtual workspace.
//
// The claim is what makes this work without the claimer holding RBAC inside every
// consumer workspace, which is what permission claims exist to avoid. The claim is
// also the whole of the permission: a claim on the parent resource does not carry
// its subresources, so a claimer that asks only for "cowboys" must not reach
// "cowboys/ssh".
func TestCustomSubresourceClaimsThroughVW(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClients, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	claimerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	cowboysGVR := schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "cowboys"}

	t.Log("Install cowboys APIResourceSchema into provider")
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Log("Install the APIResourceSchema the ssh subresource speaks")
	_, err = kcpClients.Cluster(providerPath).ApisV1alpha1().APIResourceSchemas().Create(t.Context(), sshAPIResourceSchema(), metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Create APIExport in provider, declaring cowboys and the custom subresource cowboys/ssh")
	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "ssh-cowboys"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:    "cowboys",
					Group:   "wildwest.dev",
					Schema:  "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
				},
				{
					Name:   "cowboys/ssh",
					Group:  "wildwest.dev",
					Schema: "today.ssh.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
							Reference: corev1.TypedLocalObjectReference{
								APIGroup: ptr.To(cachev1alpha1.SchemeGroupVersion.Group),
								Kind:     "ClusterCachedResourceEndpointSlice",
								Name:     unresolvableEndpointSlice,
							},
						},
					},
				},
			},
		},
	}
	_, err = kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), apiExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Bind provider APIExport in consumer")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: apiExport.Name},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{Path: providerPath.String(), Name: apiExport.Name},
				},
			},
		}, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Create a cowboy in the consumer workspace")
	cowboy := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "wildwest.dev/v1alpha1",
			"kind":       "Cowboy",
			"metadata":   map[string]interface{}{"name": "woody"},
			"spec":       map[string]interface{}{"intent": "yeehaw"},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := dynamicClusterClient.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default").Create(t.Context(), cowboy, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating cowboy: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Get the provider APIExport identity hash")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), apiExport.Name, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))
	export, err := kcpClients.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), apiExport.Name, metav1.GetOptions{})
	require.NoError(t, err)
	identityHash := export.Status.IdentityHash

	claim := func(resource string, verbs ...string) apisv1alpha2.PermissionClaim {
		return apisv1alpha2.PermissionClaim{
			GroupResource: apisv1alpha2.GroupResource{Group: "wildwest.dev", Resource: resource},
			Verbs:         verbs,
			IdentityHash:  identityHash,
		}
	}
	parentClaim := claim("cowboys", "get", "list", "watch")
	sshClaim := claim("cowboys/ssh", "create")

	// Two claimers over the same resource: one that claims the subresource and one
	// that claims only what it hangs off. The difference between them is the whole
	// of the permission.
	createClaimer := func(name string, claims ...apisv1alpha2.PermissionClaim) {
		t.Helper()

		t.Logf("Create claimer APIExport %q", name)
		_, err := kcpClients.Cluster(claimerPath).ApisV1alpha2().APIExports().Create(t.Context(), &apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       apisv1alpha2.APIExportSpec{PermissionClaims: claims},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		accepted := make([]apisv1alpha2.AcceptablePermissionClaim, 0, len(claims))
		for _, c := range claims {
			accepted = append(accepted, apisv1alpha2.AcceptablePermissionClaim{
				State: apisv1alpha2.ClaimAccepted,
				ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
					PermissionClaim: c,
					Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
				},
			})
		}

		t.Logf("Bind claimer APIExport %q in consumer, accepting its claims", name)
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := kcpClients.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), &apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{Path: claimerPath.String(), Name: name},
					},
					PermissionClaims: accepted,
				},
			}, metav1.CreateOptions{})
			return err == nil, fmt.Sprintf("error creating APIBinding: %v", err)
		}, wait.ForeverTestTimeout, time.Millisecond*100)
	}

	createClaimer("ssh-wrangler", parentClaim, sshClaim)
	createClaimer("cowboy-watcher", parentClaim)

	consumerCluster := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	t.Run("the claimed subresource is advertised by the claimer's virtual workspace", func(t *testing.T) {
		t.Parallel()

		vwCfg := vwConfig(t, cfg, kcpClients, consumerWorkspace, claimerPath, "ssh-wrangler")
		vwKcpClients, err := kcpclientset.NewForConfig(vwCfg)
		require.NoError(t, err)

		kcptestinghelpers.Eventually(t, func() (bool, string) {
			list, err := vwKcpClients.Cluster(consumerCluster.Path()).Discovery().ServerResourcesForGroupVersion(cowboysGVR.GroupVersion().String())
			if err != nil {
				return false, fmt.Sprintf("error getting discovery: %v", err)
			}
			for _, r := range list.APIResources {
				if r.Name != "cowboys/ssh" {
					continue
				}
				// The subresource is reached under the verb its HTTP method
				// implies, so a create-only claim advertises exactly "create".
				return len(r.Verbs) == 1 && r.Verbs[0] == "create",
					fmt.Sprintf("cowboys/ssh advertises %v, want [create]", r.Verbs)
			}
			return false, fmt.Sprintf("cowboys/ssh not advertised, have %v", resourceNames(list))
		}, wait.ForeverTestTimeout, time.Millisecond*100)
	})

	t.Run("a request for the claimed subresource reaches the shard's virtual-resource path", func(t *testing.T) {
		t.Parallel()

		// It cannot be served: the endpoint slice the provider names does not
		// exist, and no virtual workspace implements ssh in this test. What matters
		// is which failure comes back. A 404 would mean the virtual workspace has
		// no API for the subresource at all, which is what this change fixes.
		vwCowboys := vwResourceClient(t, cfg, kcpClients, consumerWorkspace, claimerPath, "ssh-wrangler", cowboysGVR)

		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := vwCowboys.Create(t.Context(), cowboy, metav1.CreateOptions{}, "ssh")
			if err == nil {
				return false, "expected an error: nothing serves this subresource"
			}
			if apierrors.IsNotFound(err) {
				return false, fmt.Sprintf("the virtual workspace still has no API for the claimed subresource: %v", err)
			}
			if apierrors.IsForbidden(err) {
				return false, fmt.Sprintf("claim not accepted yet: %v", err)
			}
			// The shard resolves the entry and fails on the endpoint slice the
			// provider names, which does not exist. Naming that slice is proof of
			// the whole path: the request left the virtual workspace under the
			// parent resource, was authorized as the caller on the shard, and the
			// shard resolved it to the declaring APIExport's entry.
			return apierrors.IsInternalError(err) && strings.Contains(err.Error(), unresolvableEndpointSlice),
				fmt.Sprintf("expected the endpoint-slice resolution to fail on the shard, got: %v", err)
		}, wait.ForeverTestTimeout, time.Millisecond*100)
	})

	t.Run("claiming the resource does not carry its subresources", func(t *testing.T) {
		t.Parallel()

		vwCowboys := vwResourceClient(t, cfg, kcpClients, consumerWorkspace, claimerPath, "cowboy-watcher", cowboysGVR)

		// The parent is claimed, so the resource itself is served here.
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := vwCowboys.Get(t.Context(), "woody", metav1.GetOptions{})
			return err == nil, fmt.Sprintf("waiting for the claimed parent to be served: %v", err)
		}, wait.ForeverTestTimeout, time.Millisecond*100)

		// Its subresource is not.
		_, err := vwCowboys.Create(t.Context(), cowboy, metav1.CreateOptions{}, "ssh")
		require.Error(t, err, "a claim on cowboys must not reach cowboys/ssh")
		require.True(t, apierrors.IsNotFound(err) || apierrors.IsForbidden(err),
			"expected the subresource not to be served, got: %v", err)
	})
}

func resourceNames(list *metav1.APIResourceList) []string {
	names := make([]string, 0, len(list.APIResources))
	for _, r := range list.APIResources {
		names = append(names, r.Name)
	}
	return names
}

// sshAPIResourceSchema is the schema the ssh subresource speaks. A subresource
// describes its own kind rather than its parent's, and the virtual workspace reads
// this to know which kind to advertise.
func sshAPIResourceSchema() *apisv1alpha1.APIResourceSchema {
	return &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{Name: "today.ssh.wildwest.dev"},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: "wildwest.dev",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "ssh",
				Singular: "ssh",
				Kind:     "SSHRequest",
				ListKind: "SSHRequestList",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apisv1alpha1.APIResourceVersion{{
				Name:    "v1alpha1",
				Served:  true,
				Storage: true,
				Schema: runtime.RawExtension{
					Raw: []byte(`{"type":"object","x-kubernetes-preserve-unknown-fields":true}`),
				},
			}},
		},
	}
}
