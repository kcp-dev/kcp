/*
Copyright 2024 The KCP Authors.

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

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestImpersonation(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancelFn := context.WithCancel(context.Background())
	t.Cleanup(cancelFn)

	server := framework.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	org, _ := framework.NewOrganizationFixture(t, server)
	_, wsObj := framework.NewWorkspaceFixture(t, server, org)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Log("Make user-1 an admin of the org")
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, org, []string{"user-1"}, []string{"cluster-admin"}, true)
	user1Cfg := framework.StaticTokenUserConfig("user-1", cfg)
	user1Client, err := kcpclientset.NewForConfig(user1Cfg)
	require.NoError(t, err)

	t.Logf("User-1 should not be able to edit workspace status")
	var ws *tenancyv1alpha1.Workspace
	require.Eventually(t, func() bool {
		var err error
		ws, err = user1Client.TenancyV1alpha1().Workspaces().Cluster(org).Get(ctx, wsObj.Name, metav1.GetOptions{})
		require.NoError(t, err)
		ws.Status.Phase = "Scheduling"
		_, err = user1Client.TenancyV1alpha1().Workspaces().Cluster(org).UpdateStatus(ctx, ws, metav1.UpdateOptions{})
		return apierrors.IsForbidden(err)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "user-1 should not be able to edit its own workspace status")

	user1Cfg.Impersonate = rest.ImpersonationConfig{
		UserName: "user-1",
		Groups:   []string{"system:masters"},
	}
	user1Client, err = kcpclientset.NewForConfig(user1Cfg)
	require.NoError(t, err)

	t.Logf("User-1 should NOT be able to edit workspace status with system:masters impersonation")
	require.Eventually(t, func() bool {
		ws.Status.Phase = "Scheduling"
		_, err = user1Client.TenancyV1alpha1().Workspaces().Cluster(org).UpdateStatus(ctx, ws, metav1.UpdateOptions{})
		return apierrors.IsForbidden(err)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "user-1 should NOT be able to edit its own workspace status with impersonation")
}

func TestImpersonateScoping(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancelFn := context.WithCancel(context.Background())
	t.Cleanup(cancelFn)

	server := framework.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	org, ws := framework.NewOrganizationFixture(t, server)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Log("Make user-1 an admin of the org")
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, org, []string{"user-1"}, []string{"cluster-admin"}, true)
	user1Cfg := framework.StaticTokenUserConfig("user-1", cfg)

	t.Logf("Impersonate user-1 as some group")
	user1Cfg.Impersonate = rest.ImpersonationConfig{
		UserName: "user-1",
		Groups:   []string{"elephant"},
	}
	user1Client, err := kcpkubernetesclientset.NewForConfig(user1Cfg)
	require.NoError(t, err)

	t.Logf("Scoping should be added in SelfSubjectReview")
	require.Eventually(t, func() bool {
		r, err := user1Client.AuthenticationV1().SelfSubjectReviews().Cluster(org).Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
		if err != nil {
			return false
		}

		require.Contains(t, r.Status.UserInfo.Extra["authentication.kcp.io/scopes"], "cluster:"+ws.Spec.Cluster, "scoping to cluster:%s should be added in SelfSubjectReview", ws.Spec.Cluster)
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "scoping should be added in SelfSubjectReview")
}
