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

package apibinding

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAPIBindingPermissionClaimsVersionConversion exercises native apis.kcp.io conversion by
// creating APIBindings with permission claims in one API version and reading them in the other.
// Most e2e tests only use the storage version (v1alpha2), so this path would otherwise never run.
func TestAPIBindingPermissionClaimsVersionConversion(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	exportName := "configmaps-export"
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: exportName},
		Spec: apisv1alpha2.APIExportSpec{
			PermissionClaims: []apisv1alpha2.PermissionClaim{{
				GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "configmaps"},
				Verbs:         []string{"*"},
			}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create APIExport")

	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), exportName, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid), "APIExport did not become identity-valid")

	t.Run("v1alpha2 create then v1alpha1 get", func(t *testing.T) {
		t.Parallel()

		bindingName := "from-v2"
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), &apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{Name: bindingName},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{
							Path: providerPath.String(),
							Name: exportName,
						},
					},
					PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{{
						ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
							PermissionClaim: apisv1alpha2.PermissionClaim{
								GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "configmaps"},
								Verbs:         []string{"*"},
							},
							Selector: apisv1alpha2.PermissionClaimSelector{MatchAll: true},
						},
						State: apisv1alpha2.ClaimAccepted,
					}},
				},
			}, metav1.CreateOptions{})
			return err == nil, fmt.Sprintf("create APIBinding: %v", err)
		}, wait.ForeverTestTimeout, time.Millisecond*100)

		kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
			return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), bindingName, metav1.GetOptions{})
		}, kcptestinghelpers.Is(apisv1alpha2.PermissionClaimsValid), "permission claims did not become valid")

		stored, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), bindingName, metav1.GetOptions{})
		require.NoError(t, err)
		require.Len(t, stored.Spec.PermissionClaims, 1)
		require.Equal(t, apisv1alpha2.ClaimAccepted, stored.Spec.PermissionClaims[0].State)
		require.True(t, stored.Spec.PermissionClaims[0].Selector.MatchAll)
		require.Equal(t, []string{"*"}, stored.Spec.PermissionClaims[0].Verbs)

		converted, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Get(t.Context(), bindingName, metav1.GetOptions{})
		require.NoError(t, err, "GET as v1alpha1 must succeed via scheme conversion")
		require.Len(t, converted.Spec.PermissionClaims, 1)
		require.Equal(t, apisv1alpha1.ClaimAccepted, converted.Spec.PermissionClaims[0].State)
		require.Equal(t, "configmaps", converted.Spec.PermissionClaims[0].Resource)
		require.True(t, converted.Spec.PermissionClaims[0].All, "MatchAll/* must convert to All=true")
		require.Empty(t, converted.Spec.PermissionClaims[0].ResourceSelector)
	})

	t.Run("v1alpha1 create then v1alpha2 get", func(t *testing.T) {
		t.Parallel()

		bindingName := "from-v1"
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Create(t.Context(), &apisv1alpha1.APIBinding{
				ObjectMeta: metav1.ObjectMeta{Name: bindingName},
				Spec: apisv1alpha1.APIBindingSpec{
					Reference: apisv1alpha1.BindingReference{
						Export: &apisv1alpha1.ExportBindingReference{
							Path: providerPath.String(),
							Name: exportName,
						},
					},
					PermissionClaims: []apisv1alpha1.AcceptablePermissionClaim{{
						PermissionClaim: apisv1alpha1.PermissionClaim{
							GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"},
							All:           true,
						},
						State: apisv1alpha1.ClaimAccepted,
					}},
				},
			}, metav1.CreateOptions{})
			return err == nil, fmt.Sprintf("create APIBinding: %v", err)
		}, wait.ForeverTestTimeout, time.Millisecond*100)

		kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
			return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), bindingName, metav1.GetOptions{})
		}, kcptestinghelpers.Is(apisv1alpha2.PermissionClaimsValid), "permission claims did not become valid")

		converted, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), bindingName, metav1.GetOptions{})
		require.NoError(t, err, "GET as v1alpha2 must succeed after v1alpha1 create")
		require.Len(t, converted.Spec.PermissionClaims, 1)
		require.Equal(t, apisv1alpha2.ClaimAccepted, converted.Spec.PermissionClaims[0].State)
		require.Equal(t, "configmaps", converted.Spec.PermissionClaims[0].Resource)
		require.True(t, converted.Spec.PermissionClaims[0].Selector.MatchAll, "All=true must convert to MatchAll")
		require.Equal(t, []string{"*"}, converted.Spec.PermissionClaims[0].Verbs, "v1alpha1 claims default to verbs=['*']")
	})
}
