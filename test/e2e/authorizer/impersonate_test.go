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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestImpersonation(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancelFn := context.WithCancel(context.Background())
	t.Cleanup(cancelFn)

	server := framework.SharedKcpServer(t)
	cfg := server.BaseConfig(t)
	user1workspace, _ := framework.NewOrganizationFixture(t, server)
	user2workspace, _ := framework.NewOrganizationFixture(t, server)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Log("Make user-1 an admin of the workspace1")
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, user1workspace, []string{"user-1"}, nil, true)
	user1Cfg := framework.StaticTokenUserConfig("user-1", cfg)
	user1KubeClusterClient, err := kcpkubernetesclientset.NewForConfig(user1Cfg)
	require.NoError(t, err)

	t.Log("User-1 should be able to read secrets")
	require.Eventually(t, func() bool {
		_, err := user1KubeClusterClient.Cluster(user1workspace).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, time.Millisecond*100, "user-1 should be able to read secrets")

	t.Log("Make user-2 an admin of the worksapce2")
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, user2workspace, []string{"user-2"}, nil, true)
	user2Cfg := framework.StaticTokenUserConfig("user-2", cfg)
	user2KubeClusterClient, err := kcpkubernetesclientset.NewForConfig(user2Cfg)
	require.NoError(t, err)

	t.Log("User-2 should be able to read secrets")
	require.Eventually(t, func() bool {
		_, err := user2KubeClusterClient.Cluster(user2workspace).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, time.Millisecond*100, "user-2 should be able to read secrets")

	t.Log("User-1 should NOT be able to read secrets in user-2 workspace")
	require.Eventually(t, func() bool {
		_, err := user1KubeClusterClient.Cluster(user2workspace).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
		return apierrors.IsForbidden(err)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "User-1 should NOT be able to read secrets in user-2 workspace")

	t.Log("Make user-1 impersonate user-2 as system:masters")
	impersonationConfig := rest.CopyConfig(user1Cfg)
	impersonationConfig.Impersonate = rest.ImpersonationConfig{
		UserName: "user-2",
		Groups:   []string{"system:masters"},
	}
	impersonatedClient, err := kcpkubernetesclientset.NewForConfig(impersonationConfig)
	require.NoError(t, err)

	t.Log("As user-1 with system:masters, we should NOT be able to read secrets in user 2 workspace")
	require.Eventually(t, func() bool {
		_, err := impersonatedClient.Cluster(user2workspace).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
		return apierrors.IsForbidden(err)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "as user-1 with system:masters, we should NOT be able to read secrets")

	t.Log("Make user-1 impersonate system:serviceaccount:default:kcp-rest as system:kcp:admin")
	impersonationKCPAdminConfig := rest.CopyConfig(user1Cfg)
	impersonationKCPAdminConfig.Impersonate = rest.ImpersonationConfig{
		UserName: "system:serviceaccount:default:kcp-rest",
		Groups:   []string{"system:kcp:admin"},
	}
	impersonatedKCPAdminClient, err := kcpkubernetesclientset.NewForConfig(impersonationKCPAdminConfig)
	require.NoError(t, err)

	t.Log("As user-1 with kcp-admin:system:kcp:admin, we should NOT be able to read secrets in user1 workspace")
	require.Eventually(t, func() bool {
		_, err := impersonatedKCPAdminClient.Cluster(user2workspace).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
		return apierrors.IsForbidden(err)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "as user-1 with kcp-admin:system:kcp:admin, we should NOT be able to read secrets")

	// TODO: Add test to check for warrant impersonation once https://github.com/kcp-dev/kcp/pull/3156/
	// is merged and the feature is available in the API server.
}
