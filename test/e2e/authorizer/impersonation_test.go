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
	wsPath, _ := framework.NewOrganizationFixture(t, server)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Log("Make user-1 an admin of the workspace")
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, wsPath, []string{"user-1"}, nil, true)
	user1Cfg := framework.StaticTokenUserConfig("user-1", cfg)
	user1KubeClusterClient, err := kcpkubernetesclientset.NewForConfig(user1Cfg)
	require.NoError(t, err)

	t.Log("User-1 should be able to read secrets")
	require.Eventually(t, func() bool {
		_, err := user1KubeClusterClient.Cluster(wsPath).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, time.Millisecond*100, "user-1 should be able to read secrets")

	t.Log("Make user-1 impersonate user-2 as system:masters")
	impersonationConfig := rest.CopyConfig(user1Cfg)
	impersonationConfig.Impersonate = rest.ImpersonationConfig{
		UserName: "user-2",
		Groups:   []string{"system:masters"},
	}
	impersonatedClient, err := kcpkubernetesclientset.NewForConfig(impersonationConfig)
	require.NoError(t, err)

	t.Log("As user-2 with system:masters, we should be able to read secrets")
	require.Eventually(t, func() bool {
		_, err := impersonatedClient.Cluster(wsPath).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, time.Millisecond*100, "as user-2 with system:masters, we should be able to read secrets")

	t.Logf("Check SelfSubjectReview as user-2 with system:masters")
	req := &authenticationv1.SelfSubjectReview{}
	resp, err := impersonatedClient.Cluster(wsPath).AuthenticationV1().SelfSubjectReviews().Create(ctx, req, metav1.CreateOptions{})
	require.NoError(t, err)
	require.NotContainsf(t, resp.Status.UserInfo.Groups, "system:masters", "user-2 should not have system:masters group: %s", resp.Status.UserInfo.Groups)
}
