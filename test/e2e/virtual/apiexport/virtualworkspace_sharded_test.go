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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// Concrete "cloud API" delegation example, across shards.
//
//   - The Networking provider exports a "vnets.networking.example.io" API. Users
//     create VNet objects in their own workspace.
//   - The ManagedClusters provider exports a "managedclusters.compute.example.io"
//     API and *claims* vnets, so that when it reconciles a managed cluster it can
//     read (and create) the user's VNets - e.g. to check CIDR clashes. This is
//     provider delegation.
//
// A user workspace binds both providers, creates a VNet, and accepts the
// ManagedClusters provider's vnets claim (consent). The ManagedClusters provider
// reads VNets across all such user workspaces through the managedclusters
// APIExport virtual workspace.
//
// The bug (what happens with the kuery provider and Edges): the provider watches
// *every* endpoint in the managedclusters APIExportEndpointSlice. A shard that has
// a managedclusters consumer but no Networking binding does not serve
// "vnets:<identity>" at all and used to return HTTP 404, wedging the provider's
// informer even though another shard holds the user's consented VNet. The
// forwarding storage now returns an empty list / open empty watch for a claimed
// resource not served on the shard, so that endpoint yields empty instead of 404.
// Permission-claim labels (consent) are unchanged.
//
// This test places the user (with the VNet) on a non-root shard and a
// managedclusters-only consumer on root, and requires every managedclusters VW
// endpoint to list vnets without error AND the user's VNet to be returned.
func TestAPIExportPermissionClaimsVirtualWorkspaceAcrossShards(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client")
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client")

	shards, err := kcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err, "failed to list shards")
	if len(shards.Items) < 2 {
		t.Skipf("Need at least 2 shards to run this test, got %d", len(shards.Items))
	}
	var userShard string
	for _, s := range shards.Items {
		if _, unsched := s.Annotations["experimental.core.kcp.io/unschedulable"]; unsched {
			continue
		}
		if s.Name == corev1alpha1.RootShard {
			continue
		}
		userShard = s.Name
		break
	}
	require.NotEmpty(t, userShard, "could not find a schedulable non-root shard")
	t.Logf("User (with the VNet) on shard %q; managedclusters-only consumer on root %q", userShard, corev1alpha1.RootShard)

	const (
		netGroup     = "networking.example.io"
		vnetsAPI     = "vnets"
		vnetKind     = "VNet"
		mcGroup      = "compute.example.io"
		mcAPI        = "managedclusters"
		mcKind       = "ManagedCluster"
		netExport    = "networking"
		mcExport     = "managedclusters"
		userVNetName = "user-vnet"
	)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	networkingPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("networking-provider"), kcptesting.WithRootShard())
	managedClustersPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("managedclusters-provider"), kcptesting.WithRootShard())
	// The user binds both providers, holds the VNet, on the non-root shard.
	userPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("user"), kcptesting.WithShard(userShard))
	// A second user that only enabled ManagedClusters, on root (no Networking binding).
	mcOnlyUserPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("user-mc-only"), kcptesting.WithRootShard())

	t.Logf("Create Networking provider (owns vnets) in %q", networkingPath)
	createProviderExport(t.Context(), t, kcpClusterClient, networkingPath, netExport, netGroup, vnetsAPI, vnetKind, nil)
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(networkingPath).ApisV1alpha2().APIExports().Get(t.Context(), netExport, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))
	netExportObj, err := kcpClusterClient.Cluster(networkingPath).ApisV1alpha2().APIExports().Get(t.Context(), netExport, metav1.GetOptions{})
	require.NoError(t, err)
	vnetIdentity := netExportObj.Status.IdentityHash
	require.NotEmpty(t, vnetIdentity, "expected a vnets identity hash")

	vnetClaim := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{Group: netGroup, Resource: vnetsAPI},
		IdentityHash:  vnetIdentity,
		Verbs:         []string{"*"},
	}
	t.Logf("Create ManagedClusters provider (owns managedclusters, claims vnets) in %q", managedClustersPath)
	createProviderExport(t.Context(), t, kcpClusterClient, managedClustersPath, mcExport, mcGroup, mcAPI, mcKind, []apisv1alpha2.PermissionClaim{vnetClaim})

	acceptedVNets := apisv1alpha2.AcceptablePermissionClaim{
		ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
			PermissionClaim: vnetClaim,
			Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
		},
		State: apisv1alpha2.ClaimAccepted,
	}

	// user: bind Networking, create a VNet, then bind ManagedClusters accepting the
	// vnets claim (so the VNet gets the consent label).
	vnetsGVR := schema.GroupVersionResource{Group: netGroup, Version: "v1", Resource: vnetsAPI}
	t.Logf("In user %q: bind Networking, create VNet %q, bind ManagedClusters (accept vnets claim)", userPath, userVNetName)
	bindExport(t.Context(), t, kcpClusterClient, userPath, networkingPath, netExport, netGroup, nil)
	createCloudObject(t.Context(), t, dynamicClusterClient, userPath, vnetsGVR, vnetKind, netGroup, userVNetName, map[string]interface{}{"cidr": "10.0.0.0/16"})
	bindExport(t.Context(), t, kcpClusterClient, userPath, managedClustersPath, mcExport, mcGroup, []apisv1alpha2.AcceptablePermissionClaim{acceptedVNets})

	// mc-only user: enable ManagedClusters only (no Networking binding) on root.
	t.Logf("In user %q (root): bind ManagedClusters only (no Networking)", mcOnlyUserPath)
	bindExport(t.Context(), t, kcpClusterClient, mcOnlyUserPath, managedClustersPath, mcExport, mcGroup, []apisv1alpha2.AcceptablePermissionClaim{acceptedVNets})

	t.Logf("Wait for both ManagedClusters bindings to apply the vnets claim")
	for _, u := range []struct {
		name string
		path logicalcluster.Path
	}{
		{"user", userPath},
		{"user-mc-only", mcOnlyUserPath},
	} {
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			binding, err := kcpClusterClient.Cluster(u.path).ApisV1alpha2().APIBindings().Get(t.Context(), mcExport, metav1.GetOptions{})
			require.NoError(t, err)
			for _, claim := range binding.Status.AppliedPermissionClaims {
				if claim.IdentityHash == vnetIdentity {
					return true, ""
				}
			}
			return false, fmt.Sprintf("waiting for applied vnets claim on %q", u.name)
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "the vnets claim was never applied on %q", u.name)
	}

	t.Logf("As the ManagedClusters provider, list VNets through EVERY managedclusters VW endpoint")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		slice, err := kcpClusterClient.Cluster(managedClustersPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), mcExport, metav1.GetOptions{})
		require.NoError(t, err)
		urls := framework.ExportVirtualWorkspaceURLs(slice)
		// Expect an endpoint on the user's shard and on root (mc-only user).
		if len(urls) < 2 {
			return false, fmt.Sprintf("waiting for managedclusters endpoints on both shards (got %d: %v)", len(urls), urls)
		}

		found := false
		for _, u := range urls {
			vwCfg := rest.CopyConfig(cfg)
			vwCfg.Host = u
			vwClient, err := kcpdynamic.NewForConfig(vwCfg)
			require.NoError(t, err)

			list, err := vwClient.Resource(vnetsGVR).List(t.Context(), metav1.ListOptions{})
			if err != nil {
				// The root endpoint (mc-only user, no Networking binding) must return
				// an empty list, not a 404.
				return false, fmt.Sprintf("endpoint %s errored listing vnets (expected graceful empty, not 404): %v", u, err)
			}
			for _, item := range list.Items {
				if item.GetName() == userVNetName {
					found = true
				}
			}
		}
		if !found {
			return false, "the user's VNet was not served by any managedclusters VW endpoint"
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond,
		"the ManagedClusters provider must read the user's cross-shard VNet with no endpoint returning 404")
}

// createProviderExport creates an APIResourceSchema for group/plural and an
// APIExport named exportName that owns it (plus the given permission claims).
func createProviderExport(ctx context.Context, t *testing.T, client kcpclientset.ClusterInterface, path logicalcluster.Path, exportName, group, plural, kind string, claims []apisv1alpha2.PermissionClaim) {
	t.Helper()

	s := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("v1.%s.%s", plural, group)},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   plural,
				Singular: strings.ToLower(kind),
				Kind:     kind,
				ListKind: kind + "List",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apisv1alpha1.APIResourceVersion{{
				Name:    "v1",
				Served:  true,
				Storage: true,
				Schema: runtime.RawExtension{
					Raw: jsonOrDie(t, &apiextensionsv1.JSONSchemaProps{
						Type:                   "object",
						XPreserveUnknownFields: ptr.To(true),
					}),
				},
			}},
		},
	}
	_, err := client.Cluster(path).ApisV1alpha1().APIResourceSchemas().Create(ctx, s, metav1.CreateOptions{})
	require.NoError(t, err, "creating APIResourceSchema %s|%s", path, s.Name)

	export := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: exportName},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{{
				Name:    plural,
				Group:   group,
				Schema:  s.Name,
				Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
			}},
			PermissionClaims: claims,
		},
	}
	_, err = client.Cluster(path).ApisV1alpha2().APIExports().Create(ctx, export, metav1.CreateOptions{})
	require.NoError(t, err, "creating APIExport %s|%s", path, export.Name)
}

// bindExport creates an APIBinding (named exportName) in consumerPath that
// references the export at providerPath, with optional accepted permission
// claims, and waits for the bound API group to appear in discovery.
func bindExport(ctx context.Context, t *testing.T, client kcpclientset.ClusterInterface, consumerPath, providerPath logicalcluster.Path, exportName, group string, claims []apisv1alpha2.AcceptablePermissionClaim) {
	t.Helper()

	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: exportName},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{Path: providerPath.String(), Name: exportName},
			},
			PermissionClaims: claims,
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := client.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("creating APIBinding %s|%s: %v", consumerPath, exportName, err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		groups, err := client.Cluster(consumerPath).Discovery().ServerGroups()
		if err != nil {
			return false, err.Error()
		}
		for _, g := range groups.Groups {
			if g.Name == group {
				return true, ""
			}
		}
		return false, fmt.Sprintf("waiting for %q in %q discovery", group, consumerPath)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

// createCloudObject creates an unstructured CR (with the given spec) in path.
func createCloudObject(ctx context.Context, t *testing.T, client kcpdynamic.ClusterInterface, path logicalcluster.Path, gvr schema.GroupVersionResource, kind, group, name string, spec map[string]interface{}) {
	t.Helper()

	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(group + "/" + gvr.Version)
	obj.SetKind(kind)
	obj.SetName(name)
	require.NoError(t, unstructured.SetNestedMap(obj.Object, spec, "spec"))

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := client.Cluster(path).Resource(gvr).Namespace("default").Create(ctx, obj, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("creating %s %s|%s: %v", kind, path, name, err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func jsonOrDie(t *testing.T, obj interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(obj)
	require.NoError(t, err)
	return b
}
