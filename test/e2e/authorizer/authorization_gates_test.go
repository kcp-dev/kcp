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

package authorizer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/server"
)

// TestAuthorizationWebhookCannotBypassGates proves that a webhook grant
// cannot get around the gates in the RBAC authorizer chain: with the webhook
// evaluated first and allowing everything, the required-groups gate still
// denies users outside the annotated groups, the system CRD gate still
// denies updates to APIExport/APIBinding status, and a webhook Deny
// short-circuits requests that the gates would have allowed.
//
// See https://github.com/kcp-dev/kcp/issues/4512.
func TestAuthorizationWebhookCannotBypassGates(t *testing.T) {
	framework.Suite(t, "control-plane")
	t.Parallel()

	const webhookPort = 8090
	const requiredGroup = "e2e-required-group"

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	handler := server.NewAuthzHandler(server.Allow)
	webhookCAFile, webhookStop := RunWebhook(ctx, t, webhookPort, handler)
	t.Cleanup(webhookStop)

	// The webhook is evaluated before the RBAC chain, so with the webhook
	// allowing everything, the gates are the only remaining thing that can
	// deny a request.
	srv, kcpClusterClient, kubeClusterClient := setupTest(t, "Webhook,AlwaysAllowGroups,AlwaysAllowPaths,RBAC", webhookCAFile, webhookPort)
	cfg := srv.BaseConfig(t)

	t.Log("Creating an organization gated by required groups and an ungated one")
	gatedPath, _ := framework.NewRootShardOrganizationFixture(t, srv,
		kcptesting.PrivilegedWorkspaceOption(kcptesting.WithNamePrefix("gated")),
		framework.WithRequiredGroups(requiredGroup),
	)
	orgPath, _ := framework.NewRootShardOrganizationFixture(t, srv,
		kcptesting.PrivilegedWorkspaceOption(kcptesting.WithNamePrefix("ungated")),
	)

	t.Log("Making user-1 a cluster-admin in both organizations, without the required group")
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, gatedPath, []string{"user-1"}, []string{"cluster-admin"}, true)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, orgPath, []string{"user-1"}, []string{"cluster-admin"}, true)

	user1Cfg := framework.StaticTokenUserConfig("user-1", cfg)
	user1KubeClusterClient, err := kcpkubernetesclientset.NewForConfig(user1Cfg)
	require.NoError(t, err)
	user1KcpClusterClient, err := kcpclientset.NewForConfig(user1Cfg)
	require.NoError(t, err)

	// userInGroupCfg is user-1 again, but impersonating the required group,
	// so the required-groups gate delegates instead of denying.
	userInGroupCfg := framework.StaticTokenUserConfig("user-1", cfg)
	userInGroupCfg.Impersonate = rest.ImpersonationConfig{
		UserName: "user-1",
		Groups:   []string{requiredGroup},
	}
	userInGroupKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(userInGroupCfg)
	require.NoError(t, err)

	t.Run("webhook Allow cannot bypass the required-groups gate", func(t *testing.T) {
		t.Log("Sanity: user-1 with full RBAC can list ConfigMaps in the ungated organization")
		require.Eventually(t, func() bool {
			_, err := user1KubeClusterClient.Cluster(orgPath).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
			return err == nil
		}, wait.ForeverTestTimeout, 100*time.Millisecond)

		t.Log("user-1 lacks the required group: denied by the gate despite the webhook Allow and full RBAC")
		_, err := user1KubeClusterClient.Cluster(gatedPath).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
		require.True(t, apierrors.IsForbidden(err), "expected forbidden error, got: %v", err)

		t.Log("The same user impersonating the required group is allowed again")
		_, err = userInGroupKubeClusterClient.Cluster(gatedPath).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
	})

	t.Run("webhook Allow cannot bypass the system CRD gate", func(t *testing.T) {
		t.Log("user-1 creates an APIExport in the ungated organization; creation is not gated")
		export, err := user1KcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIExports().Create(ctx, &apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: "e2e-gate-export"},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		t.Log("user-1 updates the APIExport status: denied by the gate despite the webhook Allow")
		export.Status.Phase = apisv1alpha2.APIExportPhaseReady
		_, err = user1KcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIExports().UpdateStatus(ctx, export, metav1.UpdateOptions{})
		require.True(t, apierrors.IsForbidden(err), "expected forbidden error, got: %v", err)

		t.Log("user-1 creates an APIBinding binding that export; creation is not gated")
		var binding *apisv1alpha2.APIBinding
		require.Eventually(t, func() bool {
			binding, err = user1KcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIBindings().Create(ctx, &apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "e2e-gate-binding"},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{
							Path: orgPath.String(),
							Name: "e2e-gate-export",
						},
					},
				},
			}, metav1.CreateOptions{})
			return err == nil
		}, wait.ForeverTestTimeout, 100*time.Millisecond)

		t.Log("user-1 updates the APIBinding status: denied by the gate despite the webhook Allow")
		binding.Status.Phase = apisv1alpha2.APIBindingPhaseBound
		_, err = user1KcpClusterClient.Cluster(orgPath).ApisV1alpha2().APIBindings().UpdateStatus(ctx, binding, metav1.UpdateOptions{})
		require.True(t, apierrors.IsForbidden(err), "expected forbidden error, got: %v", err)
	})

	t.Run("webhook Deny short-circuits the chain", func(t *testing.T) {
		handler.SetHandler(server.Deny)
		t.Cleanup(func() { handler.SetHandler(server.Allow) })

		t.Log("Even the user impersonating the required group is denied now: the webhook Deny wins")
		_, err := userInGroupKubeClusterClient.Cluster(gatedPath).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
		require.True(t, apierrors.IsForbidden(err), "expected forbidden error, got: %v", err)

		t.Log("And so are requests the gates would have allowed")
		_, err = user1KubeClusterClient.Cluster(orgPath).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
		require.True(t, apierrors.IsForbidden(err), "expected forbidden error, got: %v", err)
	})
}
