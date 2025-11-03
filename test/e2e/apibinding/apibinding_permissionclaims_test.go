/*
Copyright 2022 The KCP Authors.

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

package apibinding

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIBindingPermissionClaimsConditions(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, providerPath, kcpClusterClient, "wild.wild.west", "board the wanderer")

	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid), "could not wait for APIExport to be valid with identity hash")

	sheriffExport, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
	require.NoError(t, err)
	identityHash := sheriffExport.Status.IdentityHash
	wildcardVerb := []string{"*"}

	t.Logf("Found identity hash: %v", identityHash)
	apifixtures.BindToExport(ctx, t, providerPath, "wild.wild.west", consumerPath, kcpClusterClient)

	t.Logf("set up service provider with permission claims")
	setUpServiceProviderWithPermissionClaims(ctx, t, dynamicClusterClient, kcpClusterClient, providerPath, cfg, identityHash, wildcardVerb)

	t.Logf("set up binding, with invalid accepted claims hash")
	bindConsumerToProvider(ctx, t, consumerPath, providerPath, kcpClusterClient, cfg, "xxxxxxx", wildcardVerb)

	// validate the invalid claims condition occurs
	t.Logf("validate that the permission claim's conditions are false and invalid claims is the reason")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
	}, kcptestinghelpers.IsNot(apisv1alpha2.PermissionClaimsValid).WithReason(apisv1alpha2.InvalidPermissionClaimsReason), "unable to see invalid identity hash")

	t.Logf("update to correct hash")
	// have to use eventually because controllers may be modifying the APIBinding
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		binding.Spec.PermissionClaims = getAcceptedPermissionClaims(identityHash, wildcardVerb)
		_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Update(ctx, binding, metav1.UpdateOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error updating to correct hash")

	t.Logf("Validate that the permission claims are valid")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.PermissionClaimsValid), "unable to see valid claims")
	binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	if !reflect.DeepEqual(makePermissionClaims(identityHash, wildcardVerb), binding.Status.ExportPermissionClaims) {
		require.Emptyf(t, cmp.Diff(makePermissionClaims(identityHash, wildcardVerb), binding.Status.ExportPermissionClaims), "ExportPermissionClaims incorrect")
	}

	t.Logf("Validate that the permission claims were all applied")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.PermissionClaimsApplied), "unable to see claims applied")
}

func makePermissionClaims(identityHash string, verbs []string) []apisv1alpha2.PermissionClaim {
	return []apisv1alpha2.PermissionClaim{
		{
			GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "configmaps"},
			Verbs:         verbs,
		},
		{
			GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "secrets"},
			Verbs:         verbs,
		},
		{
			GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "serviceaccounts"},
			Verbs:         verbs,
		},
		{
			GroupResource: apisv1alpha2.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"},
			IdentityHash:  identityHash,
			Verbs:         verbs,
		},
		{
			GroupResource: apisv1alpha2.GroupResource{Group: authorizationv1.GroupName, Resource: "subjectaccessreviews"},
			Verbs:         verbs,
		},
		{
			GroupResource: apisv1alpha2.GroupResource{Group: authorizationv1.GroupName, Resource: "localsubjectaccessreviews"},
			Verbs:         verbs,
		},
	}
}

func setUpServiceProviderWithPermissionClaims(ctx context.Context, t *testing.T, dynamicClusterClient kcpdynamic.ClusterInterface, kcpClusterClients kcpclientset.ClusterInterface, serviceProviderWorkspace logicalcluster.Path, cfg *rest.Config, identityHash string, verbs []string) {
	t.Helper()

	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", serviceProviderWorkspace)
	serviceProviderClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Cluster(serviceProviderWorkspace).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(serviceProviderWorkspace), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "wildwest.dev",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
			PermissionClaims: makePermissionClaims(identityHash, verbs),
		},
	}
	_, err = kcpClusterClients.Cluster(serviceProviderWorkspace).ApisV1alpha2().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)
}

func getAcceptedPermissionClaims(identityHash string, verbs []string) []apisv1alpha2.AcceptablePermissionClaim {
	return []apisv1alpha2.AcceptablePermissionClaim{
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "configmaps"},
					Verbs:         verbs,
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "secrets"},
					Verbs:         verbs,
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "serviceaccounts"},
					Verbs:         verbs,
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"},
					IdentityHash:  identityHash,
					Verbs:         verbs,
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{Group: authorizationv1.GroupName, Resource: "localsubjectaccessreviews"},
					Verbs:         verbs,
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
		{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: apisv1alpha2.PermissionClaim{
					GroupResource: apisv1alpha2.GroupResource{Group: authorizationv1.GroupName, Resource: "subjectaccessreviews"},
					Verbs:         verbs,
				},
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		},
	}
}

func bindConsumerToProvider(ctx context.Context, t *testing.T, consumerWorkspace logicalcluster.Path, providerPath logicalcluster.Path, kcpClusterClients kcpclientset.ClusterInterface, cfg *rest.Config, identityHash string, verbs []string) {
	t.Helper()
	t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", consumerWorkspace, providerPath)
	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: "today-cowboys",
				},
			},
			PermissionClaims: getAcceptedPermissionClaims(identityHash, verbs),
		},
	}

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClients.Cluster(consumerWorkspace).ApisV1alpha2().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	consumerWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerWorkspace)
	err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, true, func(c context.Context) (done bool, err error) {
		groups, err := consumerWorkspaceClient.Cluster(consumerWorkspace).Discovery().ServerGroups()
		if err != nil {
			return false, fmt.Errorf("error retrieving consumer workspace %q group discovery: %w", consumerWorkspace, err)
		}
		return groupExists(groups, wildwest.GroupName), nil
	})
	require.NoError(t, err)
	t.Logf("Make sure cowboys API resource shows up in consumer workspace %q group version discovery", consumerWorkspace)
	resources, err := consumerWorkspaceClient.Cluster(consumerWorkspace).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
	require.NoError(t, err, "error retrieving consumer workspace %q API discovery", consumerWorkspace)
	require.True(t, resourceExists(resources, "cowboys"), "consumer workspace %q discovery is missing cowboys resource", consumerWorkspace)
}
