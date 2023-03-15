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

package homeworkspaces

import (
	"context"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclusterclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestUserHomeWorkspaces(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	tokenAuthFile := framework.WriteTokenAuthFile(t)
	serverArgs := framework.TestServerArgsWithTokenAuthFile(tokenAuthFile)
	serverArgs = append(serverArgs, "--home-workspaces-home-creator-groups=team-1")
	server := framework.PrivateKcpServer(t, framework.WithCustomArguments(serverArgs...))

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	kcpConfig := server.BaseConfig(t)

	clientFor := func(token string) kcpclusterclientset.ClusterInterface {
		userConfig := framework.ConfigWithToken(token, rest.CopyConfig(kcpConfig))
		client, err := kcpclusterclientset.NewForConfig(userConfig)
		require.NoError(t, err)
		return client
	}

	kcpUser1Client := clientFor("user-1-token")
	kcpUser2Client := clientFor("user-2-token")

	t.Logf("Get ~ Home workspace URL for user-1")
	user1Home, err := kcpUser1Client.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(ctx, "~", metav1.GetOptions{})
	require.NoError(t, err, "user-1 should be able to get ~ workspace")
	require.NotEqual(t, metav1.Time{}, user1Home.CreationTimestamp, "should have a creation timestamp, i.e. is not virtual")
	require.Equal(t, corev1alpha1.LogicalClusterPhaseReady, user1Home.Status.Phase, "created home workspace should be ready")

	t.Logf("Get the logical cluster inside user:user-1 (alias of ~)")
	_, err = kcpUser1Client.Cluster(logicalcluster.NewPath("user:user-1")).CoreV1alpha1().LogicalClusters().Get(ctx, "cluster", metav1.GetOptions{})
	require.NoError(t, err, "user-1 should be able to get a logical cluster in home workspace")

	t.Logf("Get ~ Home workspace URL for user-2")
	_, err = kcpUser2Client.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().Get(ctx, "~", metav1.GetOptions{})
	require.EqualError(
		t,
		err,
		`workspaces.tenancy.kcp.io "~" is forbidden: User "user-2" cannot create resource "workspaces" in API group "tenancy.kcp.io" at the cluster scope: workspace access not permitted`,
		"user-2 should not forbidden from having a home workspace auto-created",
	)
}
