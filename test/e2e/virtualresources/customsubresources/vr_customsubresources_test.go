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

// Package customsubresources covers custom subresources declared on an APIExport:
// named subresources such as "ssh" or "reboot" that a virtual workspace serves,
// independently of how the parent object is stored.
//
// What these tests do NOT cover is a custom subresource being served successfully
// end to end. That needs a virtual workspace that actually implements one, which
// lives out of tree. What they do cover is everything on this side of that
// boundary: the declaration is accepted, it reaches the binding, the parent
// resource is unaffected by its presence, and a request that names the subresource
// leaves CRD storage and enters the virtual-resource path.
package customsubresources

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
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

const (
	// unresolvableSlice names an endpoint slice that is deliberately never created.
	//
	// Resolving it is the first thing the shard does once it has decided a request
	// belongs to a virtual resource, and nothing else in the request path looks that
	// object up. Its absence therefore turns "the request entered the virtual-resource
	// branch" into an observable error, without needing a virtual workspace that
	// serves the subresource.
	unresolvableSlice = "no-such-endpointslice"

	cowboysResource = "cowboys"
)

// subresourceEntry builds the declaration under test: a custom subresource as an
// entry in its own right, named "<resource>/<subresource>" in the style of an RBAC
// rule, always virtually stored.
func subresourceEntry(subresource string) apisv1alpha2.ResourceSchema {
	return apisv1alpha2.ResourceSchema{
		Group:  wildwestv1alpha1.SchemeGroupVersion.Group,
		Name:   cowboysResource + "/" + subresource,
		Schema: "today." + subresource + ".wildwest.dev",
		Storage: apisv1alpha2.ResourceSchemaStorage{
			Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
				Reference: corev1.TypedLocalObjectReference{
					APIGroup: ptr.To(cachev1alpha1.SchemeGroupVersion.Group),
					Kind:     "ClusterCachedResourceEndpointSlice",
					Name:     unresolvableSlice,
				},
			},
		},
	}
}

//nolint:paralleltest // the subtests share one Cowboy and must run in order: it is created, then its status is written, then its subresources are queried.
func TestCustomSubresources(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client")

	kcpApiExtensionClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions cluster client")

	wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct wildwest cluster client")

	// The typed client has no way to name an arbitrary subresource, so subresource
	// requests go through the dynamic client, which takes them as trailing path
	// segments.
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client")

	cowboysGVR := wildwestv1alpha1.SchemeGroupVersion.WithResource(cowboysResource)

	//
	// Provider: a perfectly ordinary CRD-backed resource that also declares two
	// custom subresources.
	//

	gr := metav1.GroupResource{Group: wildwestv1alpha1.SchemeGroupVersion.Group, Resource: cowboysResource}

	t.Logf("Creating %s CRD in %q", gr, providerPath)
	wildwest.Create(t, providerPath, kcpApiExtensionClusterClient.ApiextensionsV1().CustomResourceDefinitions(), gr)
	kcptesting.WaitForAPIReady(t, kcpClusterClient.Cluster(providerPath).Discovery(), wildwestv1alpha1.SchemeGroupVersion)

	schema, err := apisv1alpha1.CRDToAPIResourceSchema(wildwest.CRD(t, gr), "today")
	require.NoError(t, err)

	t.Logf("Creating APIResourceSchema %s in %q", schema.Name, providerPath)
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIResourceSchemas().Create(ctx, schema, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Creating APIExport with custom subresources in %q", providerPath)
	apiExport, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "wildwest-with-subresources"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Group:  gr.Group,
					Name:   gr.Resource,
					Schema: schema.Name,
					// The parent keeps ordinary CRD storage. That is the whole point:
					// declaring a subresource must not move the object out of etcd.
					Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
				},
				subresourceEntry("ssh"),
				subresourceEntry("reboot"),
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "an APIExport may declare custom subresources on a CRD-stored resource")

	//
	// Consumer: bind it.
	//

	t.Logf("Creating APIBinding in %q", consumerPath)
	_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(ctx, &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "wildwest"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: apiExport.Name,
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Waiting for %s to appear in %q", gr, consumerPath)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		resourceList, err := kcpClusterClient.Cluster(consumerPath).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
		if err != nil {
			return false, fmt.Sprintf("failed to retrieve discovery: %v", err)
		}
		return slices.ContainsFunc(resourceList.APIResources, func(r metav1.APIResource) bool {
			return r.Name == cowboysResource
		}), fmt.Sprintf("%s not yet served in %q", gr, consumerPath)
	}, wait.ForeverTestTimeout, time.Second, "waiting for %s in %q", gr, consumerPath)

	t.Run("the subresource entry does not become a bound resource of its own", func(t *testing.T) {
		// A subresource is an entry in spec.resources[] like any other, so the risk
		// is that the binding treats "cowboys/ssh" as a resource and tries to bind a
		// CustomResourceDefinition for it.
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "wildwest", metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}

			var bound []string
			for _, r := range binding.Status.BoundResources {
				bound = append(bound, r.Resource)
			}
			if slices.Contains(bound, cowboysResource+"/ssh") || slices.Contains(bound, cowboysResource+"/reboot") {
				return false, fmt.Sprintf("a subresource was bound as a resource: %v", bound)
			}

			return slices.Contains(bound, cowboysResource), fmt.Sprintf("%s not yet bound, have %v", gr, bound)
		}, wait.ForeverTestTimeout, time.Second, "waiting for the parent to bind without its subresources")
	})

	//
	// The parent resource must behave exactly as it did before the subresources
	// were declared. This is the regression the routing change could plausibly
	// break, because it moves the test that decides whether a request belongs to
	// CRD storage.
	//

	cowboyClient := wildwestClusterClient.Cluster(consumerPath).WildwestV1alpha1().Cowboys("default")

	t.Run("the parent object is still served from CRD storage", func(t *testing.T) {
		created, err := cowboyClient.Create(ctx, &wildwestv1alpha1.Cowboy{
			ObjectMeta: metav1.ObjectMeta{Name: "woody"},
			Spec:       wildwestv1alpha1.CowboySpec{Intent: "yeehaw"},
		}, metav1.CreateOptions{})
		require.NoError(t, err, "creating the parent object must be unaffected by its subresources")
		require.NotEmpty(t, created.UID, "the object must be persisted, not synthesised by a virtual workspace")
		require.NotEmpty(t, created.ResourceVersion)

		got, err := cowboyClient.Get(ctx, "woody", metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, "yeehaw", got.Spec.Intent)

		list, err := cowboyClient.List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		require.Len(t, list.Items, 1)
	})

	t.Run("the status subresource still follows the parent into CRD storage", func(t *testing.T) {
		got, err := cowboyClient.Get(ctx, "woody", metav1.GetOptions{})
		require.NoError(t, err)

		got.Status.Result = "still riding"
		updated, err := cowboyClient.UpdateStatus(ctx, got, metav1.UpdateOptions{})
		require.NoError(t, err, "status must not be diverted to a virtual workspace")
		require.Equal(t, "still riding", updated.Status.Result)
	})

	//
	// And the subresource itself must leave CRD storage.
	//

	t.Run("a declared subresource is routed to its virtual workspace", func(t *testing.T) {
		// Before custom subresources existed, this path 404'd: the bound CRD has no
		// "ssh" subresource and apiextensions served the request. Now the shard
		// recognises it, tries to resolve the endpoint slice the declaration names,
		// and fails there instead -- which is only reachable from the virtual-resource
		// branch.
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := dynamicClusterClient.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default").
				Get(ctx, "woody", metav1.GetOptions{}, "ssh")
			if err == nil {
				return false, "expected an error: no virtual workspace is serving this subresource"
			}
			if apierrors.IsNotFound(err) {
				return false, fmt.Sprintf("still served by CRD storage, which does not know the subresource: %v", err)
			}
			return apierrors.IsInternalError(err), fmt.Sprintf("expected a resolution failure from the virtual-resource path, got: %v", err)
		}, wait.ForeverTestTimeout, time.Second, "waiting for the subresource to be routed")
	})

	t.Run("an undeclared subresource is not diverted", func(t *testing.T) {
		// Nothing declares "logs", so the request must be left to CRD storage and
		// 404 there, rather than being swept into the virtual-resource path along
		// with its declared siblings.
		_, err := dynamicClusterClient.Cluster(consumerPath).Resource(cowboysGVR).Namespace("default").
			Get(ctx, "woody", metav1.GetOptions{}, "logs")
		require.Error(t, err)
		require.True(t, apierrors.IsNotFound(err), "an undeclared subresource must 404 from CRD storage, got: %v", err)
	})
}

// TestCustomSubresourceValidation covers the declarations an APIExport must refuse.
// These are enforced by admission rather than by the CRD schema, because they depend
// on names the object's own shape already owns.
//
//nolint:paralleltest // the cases create APIExports in one shared workspace and are cheap enough not to need parallelism.
func TestCustomSubresourceValidation(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	kcpClusterClient, err := kcpclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		subresourceName string
		wantMessage     string
	}{
		"status may not be redeclared": {
			subresourceName: "status",
			wantMessage:     "declared on the APIResourceSchema",
		},
		"scale may not be redeclared": {
			subresourceName: "scale",
			wantMessage:     "declared on the APIResourceSchema",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{Name: "rejected-" + tc.subresourceName},
				Spec: apisv1alpha2.APIExportSpec{
					Resources: []apisv1alpha2.ResourceSchema{
						{
							Group:   wildwestv1alpha1.SchemeGroupVersion.Group,
							Name:    cowboysResource,
							Schema:  "today.cowboys.wildwest.dev",
							Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
						},
						subresourceEntry(tc.subresourceName),
					},
				},
			}, metav1.CreateOptions{})

			require.Error(t, err, "the APIExport should have been rejected")
			require.Contains(t, err.Error(), tc.wantMessage)
		})
	}
}
