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
	"os/exec"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWebhook(t *testing.T) {
	framework.Suite(t, "control-plane")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	// start a webhook that allows kcp to boot up
	webhookStop := runWebhook(ctx, t, "kubernetes:authz:allow")
	t.Cleanup(webhookStop)

	server := framework.PrivateKcpServer(t, framework.WithCustomArguments(
		"--authorization-webhook-config-file",
		"webhook.kubeconfig",
	))

	// create clients
	kcpConfig := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
	require.NoError(t, err, "failed to construct client for server")
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err, "failed to construct client for server")

	t.Log("Admin should be allowed to list Workspaces.")
	_, err = kcpClusterClient.Cluster(logicalcluster.NewPath("root")).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	// stop the webhook and switch to a deny policy
	webhookStop()

	webhookStop = runWebhook(ctx, t, "kubernetes:authz:deny")
	t.Cleanup(webhookStop)

	t.Log("Admin should not be allowed to list ConfigMaps.")
	_, err = kubeClusterClient.Cluster(logicalcluster.NewPath("root")).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
	require.Error(t, err)

	// access to health endpoints should still be granted based on --always-allow-paths,
	// even if the webhook rejects the request
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

	for _, endpoint := range []string{"/livez", "/readyz"} {
		req := rest.NewRequest(restClient).RequestURI(endpoint)
		t.Logf("%s should still be accessible.", req.URL().String())
		_, err := req.Do(ctx).Raw()
		require.NoError(t, err)
	}
}

func runWebhook(ctx context.Context, t *testing.T, response string) context.CancelFunc {
	args := []string{
		"--tls",
		"--response", response,
	}

	t.Logf("Starting webhook with %s policy...", response)

	ctx, cancel := context.WithCancel(ctx)

	cmd := exec.CommandContext(ctx, "httest", args...)
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("Failed to start webhook: %v", err)
	}

	// give httest a moment to boot up
	time.Sleep(2 * time.Second)

	return func() {
		t.Log("Stopping webhook...")
		cancel()
		// give it some time to shutdown
		time.Sleep(2 * time.Second)
	}
}
