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

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/server"
)

// TestWebhookGrantsPassThroughGates validates that a decision of an
// authorization webhook cannot get around the kcp gate authorizers.
func TestWebhookGrantsPassThroughGates(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	webhook := framework.StartAuthzWebhook(t.Context(), t, webhookAllowsUser("user-3"))

	srv := kcptesting.PrivateKcpServer(t,
		kcptesting.WithDefaultTokenAuthFile(t),
		kcptestingserver.WithCustomArguments(webhook.Args...),
	)
	// Avoid the known early-shutdown race (see authorizer e2e, kcp#3488).
	time.Sleep(3 * time.Second)

	cfg := srv.BaseConfig(t)

	user3Cfg := framework.StaticTokenUserConfig("user-3", cfg)
	user3, err := kcpkubernetesclientset.NewForConfig(user3Cfg)
	require.NoError(t, err)
	user3KCP, err := kcpclientset.NewForConfig(user3Cfg)
	require.NoError(t, err)

	t.Run("required groups gate", func(t *testing.T) {
		t.Parallel()
		t.Log("Create an ungated workspace and a workspace gated by required groups")
		ungatedPath, _ := kcptesting.NewWorkspaceFixture(t, srv, core.RootCluster.Path())
		gatedPath, _ := framework.NewRootShardOrganizationFixture(t, srv,
			kcptesting.PrivilegedWorkspaceOption(kcptesting.WithNamePrefix("gated")),
			framework.WithRequiredGroups("webhook-gate-required-group"),
		)

		t.Log("Control: the webhook grant works in a workspace that has no gate")
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := user3.Cluster(ungatedPath).CoreV1().Namespaces().List(t.Context(), metav1.ListOptions{})
			if err != nil {
				return false, err.Error()
			}
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "webhook allow must grant user-3 access to the ungated workspace")

		t.Log("A webhook Allow must not get user-3 past the required-groups gate")
		_, err := user3.Cluster(gatedPath).CoreV1().Namespaces().List(t.Context(), metav1.ListOptions{})
		require.Truef(t, apierrors.IsForbidden(err), "expected Forbidden from the required-groups gate, got %v", err)
	})

	t.Run("system CRD gate", func(t *testing.T) {
		t.Parallel()
		wsPath, _ := kcptesting.NewWorkspaceFixture(t, srv, core.RootCluster.Path())

		t.Log("Control: the webhook grant works for apiexports and apibindings")
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := user3KCP.Cluster(wsPath).ApisV1alpha2().APIExports().List(t.Context(), metav1.ListOptions{})
			if err != nil {
				return false, err.Error()
			}
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "webhook allow must grant user-3 access to apiexports")
		_, err := user3KCP.Cluster(wsPath).ApisV1alpha2().APIBindings().List(t.Context(), metav1.ListOptions{})
		require.NoError(t, err)

		// Authorization runs before the object is looked up, so the objects do not need to exist.
		for _, tt := range []struct {
			name string
			call func(ctx context.Context) error
		}{
			{"update apiexports/status", func(ctx context.Context) error {
				_, err := user3KCP.Cluster(wsPath).ApisV1alpha2().APIExports().UpdateStatus(ctx, &apisv1alpha2.APIExport{ObjectMeta: metav1.ObjectMeta{Name: "any"}}, metav1.UpdateOptions{})
				return err
			}},
			{"patch apiexports/status", func(ctx context.Context) error {
				_, err := user3KCP.Cluster(wsPath).ApisV1alpha2().APIExports().Patch(ctx, "any", types.MergePatchType, []byte(`{}`), metav1.PatchOptions{}, "status")
				return err
			}},
			{"update apibindings/status", func(ctx context.Context) error {
				_, err := user3KCP.Cluster(wsPath).ApisV1alpha2().APIBindings().UpdateStatus(ctx, &apisv1alpha2.APIBinding{ObjectMeta: metav1.ObjectMeta{Name: "any"}}, metav1.UpdateOptions{})
				return err
			}},
			{"patch apibindings/status", func(ctx context.Context) error {
				_, err := user3KCP.Cluster(wsPath).ApisV1alpha2().APIBindings().Patch(ctx, "any", types.MergePatchType, []byte(`{}`), metav1.PatchOptions{}, "status")
				return err
			}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				err := tt.call(t.Context())
				require.Truef(t, apierrors.IsForbidden(err), "expected Forbidden from the system CRD gate, got %v", err)
			})
		}
	})
}

// webhookAllowsUser allows everything for the given user and has no opinion on anyone else.
func webhookAllowsUser(user string) server.AuthzHandlerFunc {
	return func(sar authorizationv1.SubjectAccessReview) (authorizationv1.SubjectAccessReview, error) {
		if sar.Spec.User == user {
			return server.Allow(sar)
		}
		return server.NoOpinion(sar)
	}
}

// TestWebhookDenyBeatsRBACGrant validates that a webhook Deny ends the
// authorization chain with a denial, even for a user that RBAC would admit.
func TestWebhookDenyBeatsRBACGrant(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	webhook := framework.StartAuthzWebhook(t.Context(), t, server.NoOpinion)

	// The webhook has to be consulted ahead of RBAC, otherwise RBAC answers first.
	args := append([]string{}, webhook.Args...)
	args = append(args, "--authorization-order", "Webhook,RBAC")

	srv := kcptesting.PrivateKcpServer(t,
		kcptesting.WithDefaultTokenAuthFile(t),
		kcptestingserver.WithCustomArguments(args...),
	)
	// Avoid the known early-shutdown race (see authorizer e2e, kcp#3488).
	time.Sleep(3 * time.Second)

	cfg := srv.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)
	user2, err := kcpkubernetesclientset.NewForConfig(framework.StaticTokenUserConfig("user-2", cfg))
	require.NoError(t, err)

	wsPath, _ := kcptesting.NewWorkspaceFixture(t, srv, core.RootCluster.Path())

	t.Log("Admit user-2 as admin through RBAC")
	framework.AdmitWorkspaceAccess(t.Context(), t, kubeClusterClient, wsPath, []string{"user-2"}, nil, true)

	t.Log("Control: with no webhook opinion RBAC grants user-2 access")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := user2.Cluster(wsPath).CoreV1().Namespaces().List(t.Context(), metav1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "RBAC must grant user-2 access while the webhook has no opinion")

	t.Log("The webhook now denies user-2")
	webhook.Handler.SetHandler(webhookDeniesUser("user-2"))

	_, err = user2.Cluster(wsPath).CoreV1().Namespaces().List(t.Context(), metav1.ListOptions{})
	require.Truef(t, apierrors.IsForbidden(err), "expected Forbidden from the webhook Deny despite the RBAC grant, got %v", err)
}

// webhookDeniesUser denies everything for the given user and has no opinion on anyone else.
func webhookDeniesUser(user string) server.AuthzHandlerFunc {
	return func(sar authorizationv1.SubjectAccessReview) (authorizationv1.SubjectAccessReview, error) {
		if sar.Spec.User == user {
			return server.Deny(sar)
		}
		return server.NoOpinion(sar)
	}
}
