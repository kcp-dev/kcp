/*
Copyright 2024 The kcp Authors.

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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	authenticationv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
	"k8s.io/client-go/rest"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestImpersonationWarrantScoping is the regression test for warrant forgery
// through impersonation: a user who is admin of their own workspace, and hence
// allowed to impersonate there, must not be able to attach an unscoped
// authorization.kcp.io/warrant carrying a privileged group and have that
// warrant honored in other workspaces. The server scopes client-supplied
// warrants to the workspace the impersonation happens in.
//
// The cross-workspace check used here is the "bind" permission the APIBinding
// admission evaluates in the provider workspace.
func TestImpersonationWarrantScoping(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancelFn := context.WithCancel(context.Background())
	t.Cleanup(cancelFn)

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	basePath, _ := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"), kcptesting.WithNamePrefix("warrant-forgery"))
	providerPath, providerWS := kcptesting.NewWorkspaceFixture(t, server, basePath, kcptesting.WithName("provider"))
	consumerPath, consumerWS := kcptesting.NewWorkspaceFixture(t, server, basePath, kcptesting.WithName("consumer"))

	t.Logf("Create an APIExport in provider workspace %q without granting anybody bind permissions", providerPath)
	export := &apisv1alpha2.APIExport{ObjectMeta: metav1.ObjectMeta{Name: "forgery-target"}}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, export, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Make user-1 admin of consumer workspace %q only", consumerPath)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, consumerPath, []string{"user-1"}, nil, true)

	t.Log("Impersonate user-1 as itself, with a forged, unscoped warrant for system:kcp:admin")
	forgedCfg := framework.StaticTokenUserConfig("user-1", cfg)
	forgedCfg.Impersonate = rest.ImpersonationConfig{
		UserName: "user-1",
		Extra: map[string][]string{
			rbacregistryvalidation.WarrantExtraKey: {`{"user":"forged","groups":["system:kcp:admin"]}`},
		},
	}
	forgedKcpClient, err := kcpclientset.NewForConfig(forgedCfg)
	require.NoError(t, err)
	forgedKubeClient, err := kcpkubernetesclientset.NewForConfig(forgedCfg)
	require.NoError(t, err)

	t.Log("The impersonation itself works: user-1 is admin in consumer and can list APIBindings there")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := forgedKcpClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().List(ctx, metav1.ListOptions{})
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "user-1 should be able to list APIBindings in consumer")

	t.Log("The forged warrant is scoped to consumer: it grants nothing in the provider workspace")
	_, err = forgedKcpClient.Cluster(providerPath).ApisV1alpha2().APIExports().List(ctx, metav1.ListOptions{})
	require.True(t, apierrors.IsForbidden(err), "expected forbidden listing APIExports in provider with forged warrant, got: %v", err)

	t.Log("The forged warrant does not satisfy the bind check in provider: binding the export from consumer is forbidden")
	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "forgery-target"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: export.Name,
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := forgedKcpClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
		if err == nil || apierrors.IsAlreadyExists(err) {
			return false, "binding with forged warrant unexpectedly succeeded: the warrant was not scoped to the consumer workspace"
		}
		if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "no permission to bind") {
			return false, fmt.Sprintf("expected bind permission denial, got: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "binding with forged warrant must be forbidden by the bind check")

	t.Log("Control: the same unscoped warrant is confined to consumer, so granting bind to system:cluster:<consumer> in provider makes it work")
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "bind-forgery-target"},
		Rules: []rbacv1.PolicyRule{{
			Verbs:         []string{"bind"},
			APIGroups:     []string{apisv1alpha2.SchemeGroupVersion.Group},
			Resources:     []string{"apiexports"},
			ResourceNames: []string{export.Name},
		}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "bind-forgery-target"},
		Subjects:   []rbacv1.Subject{{Kind: "Group", Name: "system:cluster:" + consumerWS.Spec.Cluster}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.SchemeGroupVersion.Group, Kind: "ClusterRole", Name: "bind-forgery-target"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := forgedKcpClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return true, ""
		}
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "binding must succeed once system:cluster:<consumer> may bind")

	t.Log("The scoped warrant shows up in SelfSubjectReview with the consumer scope added")
	review, err := forgedKubeClient.Cluster(consumerPath).AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	require.NoError(t, err)
	warrants := review.Status.UserInfo.Extra[rbacregistryvalidation.WarrantExtraKey]
	require.Len(t, warrants, 1)
	require.Contains(t, warrants[0], `"authentication.kcp.io/scopes":["cluster:`+consumerWS.Spec.Cluster+`"]`, "warrant should have been scoped to the consumer cluster")

	t.Log("A warrant for a service account claiming to live in the provider cluster is rejected outright: scoping cannot neutralize the global service account identity")
	saWarrantCfg := framework.StaticTokenUserConfig("user-1", cfg)
	saWarrantCfg.Impersonate = rest.ImpersonationConfig{
		UserName: "user-1",
		Extra: map[string][]string{
			rbacregistryvalidation.WarrantExtraKey: {`{"user":"system:serviceaccount:default:controller","extra":{"` + serviceaccount.ClusterNameKey + `":["` + providerWS.Spec.Cluster + `"]}}`},
		},
	}
	saWarrantClient, err := kcpkubernetesclientset.NewForConfig(saWarrantCfg)
	require.NoError(t, err)
	_, err = saWarrantClient.Cluster(consumerPath).AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	require.True(t, apierrors.IsForbidden(err), "expected forbidden, got: %v", err)
	require.Contains(t, err.Error(), serviceaccount.ClusterNameKey)

	t.Log("Impersonating a service account that claims to live in the provider cluster is rejected as well")
	saCfg := framework.StaticTokenUserConfig("user-1", cfg)
	saCfg.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:default:controller",
		Extra:    map[string][]string{serviceaccount.ClusterNameKey: {providerWS.Spec.Cluster}},
	}
	saClient, err := kcpkubernetesclientset.NewForConfig(saCfg)
	require.NoError(t, err)
	_, err = saClient.Cluster(consumerPath).AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	require.True(t, apierrors.IsForbidden(err), "expected forbidden, got: %v", err)

	t.Log("Control: impersonating a service account of the consumer cluster itself is allowed")
	localSACfg := framework.StaticTokenUserConfig("user-1", cfg)
	localSACfg.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:default:controller",
		Extra:    map[string][]string{serviceaccount.ClusterNameKey: {consumerWS.Spec.Cluster}},
	}
	localSAClient, err := kcpkubernetesclientset.NewForConfig(localSACfg)
	require.NoError(t, err)
	_, err = localSAClient.Cluster(consumerPath).AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	require.NoError(t, err)
}

// TestImpersonationReservedExtraRejected verifies that only system:masters may
// impersonate kcp-reserved extra keys that have no scoped semantics, while
// scopes keep being accepted from everybody.
func TestImpersonationReservedExtraRejected(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancelFn := context.WithCancel(context.Background())
	t.Cleanup(cancelFn)

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	org, ws := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Log("Make user-1 an admin of the org")
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, org, []string{"user-1"}, []string{"cluster-admin"}, true)

	selfReview := func(c kcpkubernetesclientset.ClusterInterface, path logicalcluster.Path) error {
		_, err := c.AuthenticationV1().SelfSubjectReviews().Cluster(path).Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
		return err
	}

	t.Log("user-1 may impersonate with a scopes extra")
	scopedCfg := framework.StaticTokenUserConfig("user-1", cfg)
	scopedCfg.Impersonate = rest.ImpersonationConfig{
		UserName: "user-1",
		Extra:    map[string][]string{rbacregistryvalidation.ScopeExtraKey: {"cluster:" + ws.Spec.Cluster}},
	}
	scopedClient, err := kcpkubernetesclientset.NewForConfig(scopedCfg)
	require.NoError(t, err)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		err := selfReview(scopedClient, org)
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "impersonation with a scopes extra should be allowed")

	t.Log("user-1 may not impersonate a reserved kcp.io extra")
	reservedCfg := framework.StaticTokenUserConfig("user-1", cfg)
	reservedCfg.Impersonate = rest.ImpersonationConfig{
		UserName: "user-1",
		Extra:    map[string][]string{"authentication.kcp.io/reserved": {"value"}},
	}
	reservedClient, err := kcpkubernetesclientset.NewForConfig(reservedCfg)
	require.NoError(t, err)
	err = selfReview(reservedClient, org)
	require.True(t, apierrors.IsForbidden(err), "expected forbidden, got: %v", err)
	require.Contains(t, err.Error(), "is not allowed for the requestor")

	t.Log("system:masters may impersonate a reserved kcp.io extra")
	mastersCfg := server.RootShardSystemMasterBaseConfig(t)
	mastersCfg.Impersonate = rest.ImpersonationConfig{
		UserName: "puppet",
		Extra:    map[string][]string{"authentication.kcp.io/reserved": {"value"}},
	}
	mastersClient, err := kcpkubernetesclientset.NewForConfig(mastersCfg)
	require.NoError(t, err)
	// root is always served by the root shard, which is what the system:masters config talks to.
	require.NoError(t, selfReview(mastersClient, core.RootCluster.Path()))
}
