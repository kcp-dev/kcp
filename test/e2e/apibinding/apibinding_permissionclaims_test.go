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
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIBindingPermissionClaimsConditions(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	serviceProviderWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName.Path())
	consumerWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName.Path())

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, serviceProviderWorkspace.Path(), kcpClusterClient, "wild.wild.west", "board the wanderer")

	identityHash := ""
	framework.Eventually(t, func() (done bool, str string) {
		sheriffExport, err := kcpClusterClient.Cluster(serviceProviderWorkspace.Path()).ApisV1alpha1().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		if conditions.IsTrue(sheriffExport, apisv1alpha1.APIExportIdentityValid) {
			identityHash = sheriffExport.Status.IdentityHash
			return true, ""
		}
		condition := conditions.Get(sheriffExport, apisv1alpha1.APIExportIdentityValid)
		if condition != nil {
			return false, fmt.Sprintf("not done waiting for API Export condition status:%v - reason: %v - message: %v", condition.Status, condition.Reason, condition.Message)
		}
		return false, "not done waiting for APIExportIdentity to be marked valid, no condition exists"

	}, wait.ForeverTestTimeout, 100*time.Millisecond, "could not wait for APIExport to be valid with identity hash")

	t.Logf("Found identity hash: %v", identityHash)
	apifixtures.BindToExport(ctx, t, serviceProviderWorkspace, "wild.wild.west", consumerWorkspace.Path(), kcpClusterClient)

	t.Logf("set up service provider with permission claims")
	setUpServiceProviderWithPermissionClaims(ctx, dynamicClusterClient, kcpClusterClient, serviceProviderWorkspace, cfg, t, identityHash)

	t.Logf("set up binding, with invalid accepted claims hash")
	bindConsumerToProvider(ctx, consumerWorkspace.Path(), serviceProviderWorkspace, t, kcpClusterClient, cfg, "xxxxxxx")

	// validate the invalid claims condition occurs
	t.Logf("validate that the permission claim's conditions are false and invalid claims is the reason")

	framework.Eventually(t, func() (bool, string) {
		// get the binding
		binding, err := kcpClusterClient.Cluster(consumerWorkspace.Path()).ApisV1alpha1().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		cond := conditions.Get(binding, apisv1alpha1.PermissionClaimsValid)
		if cond == nil {
			return false, "not done waiting for permission claims to be invalid, no condition exits"
		}

		if cond.Status == v1.ConditionFalse && cond.Reason == apisv1alpha1.InvalidPermissionClaimsReason {
			return true, ""
		}
		return false, fmt.Sprintf("not done waiting for condition to be invalid reason: %v - message: %v", cond.Reason, cond.Message)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "unable to see invalid identity hash")

	t.Logf("update to correct hash")
	// have to use eventually because controllers may be modifying the APIBinding
	framework.Eventually(t, func() (success bool, reason string) {
		binding, err := kcpClusterClient.Cluster(consumerWorkspace.Path()).ApisV1alpha1().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		binding.Spec.PermissionClaims = getAcceptedPermissionClaims(identityHash)
		_, err = kcpClusterClient.Cluster(consumerWorkspace.Path()).ApisV1alpha1().APIBindings().Update(ctx, binding, metav1.UpdateOptions{})
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error updating to correct hash")

	t.Logf("Validate that the permission claims are valid")
	framework.Eventually(t, func() (bool, string) {
		// get the binding
		binding, err := kcpClusterClient.Cluster(consumerWorkspace.Path()).ApisV1alpha1().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		cond := conditions.Get(binding, apisv1alpha1.PermissionClaimsValid)
		if cond == nil {
			return false, "not done waiting for permission claims to be valid, no condition exits"
		}

		if cond.Status != v1.ConditionTrue {
			return false, fmt.Sprintf("not done waiting for the condition to be valid, reason: %v - message: %v", cond.Reason, cond.Message)
		}
		if !reflect.DeepEqual(makePermissionClaims(identityHash), binding.Status.ExportPermissionClaims) {
			return false, fmt.Sprintf("ExportPermissionClaims unexpected %v", cmp.Diff(makePermissionClaims(identityHash), binding.Status.ExportPermissionClaims))
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "unable to see valid claims condition")

	t.Logf("Validate that the permission claims were all applied")
	framework.Eventually(t, func() (bool, string) {
		// get the binding
		binding, err := kcpClusterClient.Cluster(consumerWorkspace.Path()).ApisV1alpha1().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		cond := conditions.Get(binding, apisv1alpha1.PermissionClaimsApplied)
		if cond == nil {
			return false, "not done waiting for permission claims to be applied, no condition exits"
		}

		if cond.Status == v1.ConditionTrue {
			return true, ""
		}
		return false, fmt.Sprintf("not done waiting for the condition to be valid, reason: %v - message: %v", cond.Reason, cond.Message)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "unable to see valid claims condition")

}

func makePermissionClaims(identityHash string) []apisv1alpha1.PermissionClaim {
	return []apisv1alpha1.PermissionClaim{
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
			All:           true,
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "secrets"},
			All:           true,
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "serviceaccounts"},
			All:           true,
		},
		{
			GroupResource: apisv1alpha1.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"},
			All:           true,
			IdentityHash:  identityHash,
		},
	}
}

func setUpServiceProviderWithPermissionClaims(ctx context.Context, dynamicClusterClient kcpdynamic.ClusterInterface, kcpClusterClients kcpclientset.ClusterInterface, serviceProviderWorkspace tenancy.Cluster, cfg *rest.Config, t *testing.T, identityHash string) {
	t.Logf("Install today cowboys APIResourceSchema into service provider workspace %q", serviceProviderWorkspace)
	serviceProviderClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Cluster(serviceProviderWorkspace.Path()).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(serviceProviderWorkspace.Path()), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
			PermissionClaims:      makePermissionClaims(identityHash),
		},
	}
	_, err = kcpClusterClients.Cluster(serviceProviderWorkspace.Path()).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)
}

func getAcceptedPermissionClaims(identityHash string) []apisv1alpha1.AcceptablePermissionClaim {
	return []apisv1alpha1.AcceptablePermissionClaim{
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
				All:           true,
			},
			State: apisv1alpha1.ClaimAccepted,
		},
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "secrets"},
				All:           true,
			},
			State: apisv1alpha1.ClaimAccepted,
		},
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "serviceaccounts"},
				All:           true,
			},
			State: apisv1alpha1.ClaimAccepted,
		},
		{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"},
				IdentityHash:  identityHash,
				All:           true,
			},
			State: apisv1alpha1.ClaimAccepted,
		},
	}
}

func bindConsumerToProvider(ctx context.Context, consumerWorkspace logicalcluster.Name, providerClusterName tenancy.Cluster, t *testing.T, kcpClusterClients kcpclientset.ClusterInterface, cfg *rest.Config, identityHash string) {
	t.Logf("Create an APIBinding in consumer workspace %q that points to the today-cowboys export from %q", consumerWorkspace, providerClusterName)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Cluster: providerClusterName,
					Name:    "today-cowboys",
				},
			},
			PermissionClaims: getAcceptedPermissionClaims(identityHash),
		},
	}

	_, err := kcpClusterClients.Cluster(consumerWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	consumerWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Make sure %q API group shows up in consumer workspace %q group discovery", wildwest.GroupName, consumerWorkspace)
	err = wait.PollImmediateWithContext(ctx, 100*time.Millisecond, wait.ForeverTestTimeout, func(c context.Context) (done bool, err error) {
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
