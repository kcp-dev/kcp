/*
Copyright 2025 The KCP Authors.

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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAuthorizationOrder(t *testing.T) {
	framework.Suite(t, "control-plane")
	t.Parallel()
	t.Run("Authorization order 1", func(t *testing.T) {
		webhookPort := "8080"
		ctx, cancelFunc := context.WithCancel(context.Background())
		t.Cleanup(cancelFunc)
		webhook1Stop := RunWebhook(ctx, t, webhookPort, "kubernetes:authz:allow")
		t.Cleanup(webhook1Stop)

		server, kcpClusterClient, kubeClusterClient := setupTest(t, "AlwaysAllowGroups,AlwaysAllowPaths,Webhook,RBAC", "testdata/webhook1.kubeconfig")

		t.Log("Admin should be allowed to list Workspaces.")
		_, err := kcpClusterClient.Cluster(logicalcluster.NewPath("root")).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		// stop the webhook and switch to a deny policy
		webhook1Stop()
		webhook2Stop := RunWebhook(ctx, t, webhookPort, "kubernetes:authz:deny")
		t.Cleanup(webhook2Stop)

		t.Log("Admin should not be allowed to list ConfigMaps.")
		_, err = kubeClusterClient.Cluster(logicalcluster.NewPath("root")).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
		require.Error(t, err)
		// access to health endpoints should still be granted based on --always-allow-paths,
		// even if the webhook rejects the request
		t.Log("Verify that it is allowed to access one of AllowAllPaths endpoints.")
		verifyEndpointAccess(ctx, t, server, "/healthz", true)
	})

	t.Run("Authorization order 2", func(t *testing.T) {
		webhookPort := "8081"
		ctx, cancelFunc := context.WithCancel(context.Background())
		t.Cleanup(cancelFunc)
		webhook1Stop := RunWebhook(ctx, t, webhookPort, "kubernetes:authz:allow")
		t.Cleanup(webhook1Stop)

		server, kcpClusterClient, kubeClusterClient := setupTest(t, "Webhook,AlwaysAllowGroups,AlwaysAllowPaths,RBAC", "testdata/webhook2.kubeconfig")

		t.Log("Verify that it is allowed to access one of AllowAllPaths endpoints.")
		verifyEndpointAccess(ctx, t, server, "/livez", true)

		t.Log("Admin should be allowed now to list Workspaces.")
		_, err := kcpClusterClient.Cluster(logicalcluster.NewPath("root")).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		// stop the webhook and switch to a deny policy
		webhook1Stop()
		webhook2Stop := RunWebhook(ctx, t, webhookPort, "kubernetes:authz:deny")
		t.Cleanup(webhook2Stop)

		t.Log("Admin should not be allowed now to list Logical clusters.")
		_, err = kcpClusterClient.Cluster(logicalcluster.NewPath("root")).CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
		require.Error(t, err)

		t.Log("Admin should not be allowed to list Services.")
		_, err = kubeClusterClient.Cluster(logicalcluster.NewPath("root")).CoreV1().Services("default").List(ctx, metav1.ListOptions{})
		require.Error(t, err)

		t.Log("Verify that it is not allowed to access one of AllowAllPaths endpoints.")
		verifyEndpointAccess(ctx, t, server, "/readyz", false)
	})

	t.Run("Default authorization order", func(t *testing.T) {
		webhookPort := "8082"
		ctx, cancelFunc := context.WithCancel(context.Background())
		t.Cleanup(cancelFunc)
		webhookStop := RunWebhook(ctx, t, webhookPort, "kubernetes:authz:deny")
		t.Cleanup(webhookStop)
		// This will setup the test with the default authorization order: AlwaysAllowGroups,AlwaysAllowPaths,RBAC,Webhook
		server, kcpClusterClient, _ := setupTest(t, "", "testdata/webhook3.kubeconfig")

		t.Log("Verify that it is allowed to access one of AllowAllPaths endpoints.")
		verifyEndpointAccess(ctx, t, server, "/healthz", true)

		t.Log("Admin should be allowed to list Workspaces.")
		_, err := kcpClusterClient.Cluster(logicalcluster.NewPath("root")).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
	})
}

func setupTest(t *testing.T, authOrder, webhookConfigFile string) (kcptestingserver.RunningServer, kcpclientset.ClusterInterface, kcpkubernetesclientset.ClusterInterface) {
	args := []string{
		"--authorization-webhook-config-file", webhookConfigFile,
	}
	if authOrder != "" {
		args = append(args, "--authorization-order", authOrder)
	}

	server := kcptesting.PrivateKcpServer(t, kcptestingserver.WithCustomArguments(args...))

	// The testing framework has a rare race condition where if you stop kcp too early after it became "ready",
	// it will run into loads of shutdown issues and the shutdown will take 3-4 minutes.
	// This can be easily avoided by simply waiting a few seconds here. Since the tests that use setupTest()
	// are very, very short anyway, this will not harm the test runtime overall, but make them much more
	// stable on some certain PCs/laptops.
	// See https://github.com/kcp-dev/kcp/issues/3488 for more information.
	time.Sleep(3 * time.Second)

	kcpConfig := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)

	return server, kcpClusterClient, kubeClusterClient
}

func verifyEndpointAccess(ctx context.Context, t *testing.T, server kcptestingserver.RunningServer, endpoint string, shouldSucceed bool) {
	rootShardCfg := server.RootShardSystemMasterBaseConfig(t)
	if rootShardCfg.NegotiatedSerializer == nil {
		rootShardCfg.NegotiatedSerializer = kubernetesscheme.Codecs.WithoutConversion()
	}
	// Ensure the request is unauthenticated, as Kubernetes' webhook authorizer is wrapped
	// in a reloadable authorizer that also always injects a privilegedGroup authorizer
	// that lets system:masters users in.
	rootShardCfg.BearerToken = ""

	restClient, err := rest.UnversionedRESTClientFor(rootShardCfg)
	require.NoError(t, err)

	req := rest.NewRequest(restClient).RequestURI(endpoint)
	t.Logf("Verifying access to: %s", req.URL().String())
	_, err = req.Do(ctx).Raw()
	if shouldSucceed {
		require.NoError(t, err)
	} else {
		require.Error(t, err)
	}
}
