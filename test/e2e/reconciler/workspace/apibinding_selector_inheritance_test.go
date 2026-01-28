/*
Copyright 2026 The KCP Authors.

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

package workspace

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIBindingSelectorInheritance(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	serviceProviderClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Create WorkspaceType in root workspace with DefaultAPIBinding")
	workspaceType := &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-consumer-type",
		},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			Extend: tenancyv1alpha1.WorkspaceTypeExtension{
				With: []tenancyv1alpha1.WorkspaceTypeReference{
					{
						Path: core.RootCluster.Path().String(),
						Name: "universal",
					},
				},
			},
			DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
				{
					Path:   "",
					Export: "today-cowboys",
				},
			},
		},
	}
	workspaceType, err = kcpClusterClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().WorkspaceTypes().Create(ctx, workspaceType, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create workspace type")

	orgsPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"), kcptesting.WithName("orgs"))
	platformPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithName("platform"))

	t.Logf("Install cowboys APIResourceSchema into platform workspace %q", platformPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Cluster(platformPath).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(platformPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(platformPath), mapper, nil, "clusterrole_cowboys.yaml", testFiles)
	require.NoError(t, err)

	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(platformPath), mapper, nil, "clusterrolebinding_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport with permission claims")
	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Group:  "wildwest.dev",
					Name:   "cowboys",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "",
						Resource: "configmaps",
					},
					Verbs: []string{"get", "list"},
				},
			},
		},
	}
	apiExport, err = kcpClusterClient.Cluster(platformPath).ApisV1alpha2().APIExports().Create(ctx, apiExport, metav1.CreateOptions{})
	require.NoError(t, err)

	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(platformPath).ApisV1alpha2().APIExports().Get(ctx, apiExport.Name, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid), "could not wait for APIExport to be valid with identity hash")

	apiExport, err = kcpClusterClient.Cluster(platformPath).ApisV1alpha2().APIExports().Get(ctx, apiExport.Name, metav1.GetOptions{})
	require.NoError(t, err)
	identityHash := apiExport.Status.IdentityHash
	require.NotEmpty(t, identityHash, "APIExport should have identity hash")

	t.Logf("Create APIBinding in orgs workspace %q with label selector", orgsPath)
	orgsBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: platformPath.String(),
					Name: apiExport.Name,
				},
			},
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{
								Group:    "",
								Resource: "configmaps",
							},
							Verbs: []string{"get", "list"},
						},
						Selector: apisv1alpha2.PermissionClaimSelector{
							LabelSelector: metav1.LabelSelector{
								MatchLabels: map[string]string{
									"platform-mesh.io/enabled": "true",
								},
							},
						},
					},
					State: apisv1alpha2.ClaimAccepted,
				},
			},
		},
	}

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(orgsPath).ApisV1alpha2().APIBindings().Create(ctx, orgsBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating orgs APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create orgs APIBinding")

	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(orgsPath).ApisV1alpha2().APIBindings().Get(ctx, orgsBinding.Name, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted), "orgs APIBinding should be completed")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		wt, err := kcpClusterClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().WorkspaceTypes().Get(ctx, workspaceType.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to get workspace type: %v", err)
		}
		wt.Spec.DefaultAPIBindings[0].Path = platformPath.String()
		_, err = kcpClusterClient.Cluster(core.RootCluster.Path()).TenancyV1alpha1().WorkspaceTypes().Update(ctx, wt, metav1.UpdateOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to update workspace type: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to update workspace type with platform path")

	t.Logf("Create child workspace in orgs with WorkspaceType - APIBinding should inherit selector from orgs")
	childPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgsPath, kcptesting.WithType(core.RootCluster.Path(), tenancyv1alpha1.WorkspaceTypeName(workspaceType.Name)), kcptesting.WithName("child"))

	t.Logf("Verify that child workspace APIBinding inherited selector from parent")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		bindings, err := kcpClusterClient.Cluster(childPath).ApisV1alpha2().APIBindings().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("failed to list APIBindings: %v", err)
		}

		for _, binding := range bindings.Items {
			if binding.Spec.Reference.Export == nil {
				continue
			}
			if binding.Spec.Reference.Export.Name == apiExport.Name &&
				binding.Spec.Reference.Export.Path == platformPath.String() {
				for _, claim := range binding.Spec.PermissionClaims {
					if claim.Group == "" && claim.Resource == "configmaps" &&
						claim.State == apisv1alpha2.ClaimAccepted {
						if claim.Selector.MatchAll {
							return false, fmt.Sprintf("APIBinding has MatchAll selector instead of inherited label selector. Full binding: %+v", binding)
						}
						if claim.Selector.LabelSelector.MatchLabels == nil {
							return false, fmt.Sprintf("APIBinding should have MatchLabels selector inherited from parent. Selector: %+v", claim.Selector)
						}
						expectedLabel := "platform-mesh.io/enabled"
						expectedValue := "true"
						if claim.Selector.LabelSelector.MatchLabels[expectedLabel] != expectedValue {
							return false, fmt.Sprintf("APIBinding should have inherited selector with label %s=%s, got %v. Full selector: %+v",
								expectedLabel, expectedValue, claim.Selector.LabelSelector.MatchLabels, claim.Selector)
						}
						return true, "APIBinding correctly inherited selector from parent"
					}
				}
				return false, fmt.Sprintf("APIBinding found but permission claim not found or not accepted. Claims: %+v", binding.Spec.PermissionClaims)
			}
		}
		return false, fmt.Sprintf("APIBinding not found yet. Found bindings: %d", len(bindings.Items))
	}, wait.ForeverTestTimeout, time.Second*2, "failed to verify selector inheritance")

	t.Logf("Successfully verified that child workspace APIBinding inherited selector from orgs workspace")
}
