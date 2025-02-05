/* /*
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

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAuthorizationModes(t *testing.T) {
	framework.Suite(t, "control-plane")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)
	// start a webhook that allows kcp to boot up
	webhookStop := RunWebhook(ctx, t, "8081", "kubernetes:authz:allow")
	t.Cleanup(webhookStop)

	server := framework.PrivateKcpServer(t, framework.WithCustomArguments(
		"--authorization-modes",
		"Webhook,AlwaysAllowPaths,AlwaysAllowGroups,RBAC",
		"--authorization-webhook-config-file",
		"authmodes.kubeconfig",
	))

	// create clients
	kcpConfig := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
	require.NoError(t, err, "failed to construct client for server")
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err, "failed to construct client for server")

	// access to health endpoints should not be granted, as webhook is first
	// in the order of authorizers and rejects the request
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

	t.Log("Verify that you are allowed to access AllowAllPaths endpoints.")
	for _, endpoint := range []string{"/livez", "/readyz"} {
		req := rest.NewRequest(restClient).RequestURI(endpoint)
		t.Logf("%s should not be accessible.", req.URL().String())
		_, err := req.Do(ctx).Raw()
		require.NoError(t, err)
	}

	t.Log("Admin should be allowed now to list Workspaces.")
	_, err = kcpClusterClient.Cluster(logicalcluster.NewPath("root")).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	// run the webhook with deny policy
	webhookStop = RunWebhook(ctx, t, "8081", "kubernetes:authz:deny")
	t.Cleanup(webhookStop)

	t.Log("Admin should not be allowed now to list Workspaces.")
	_, err = kcpClusterClient.Cluster(logicalcluster.NewPath("root")).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
	require.Error(t, err)

	t.Log("Admin should not be allowed to list ConfigMaps.")
	_, err = kubeClusterClient.Cluster(logicalcluster.NewPath("root")).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
	require.Error(t, err)

	t.Log("Verify that it is not allowed to access AllowAllPaths endpoints.")
	for _, endpoint := range []string{"/livez", "/readyz"} {
		req := rest.NewRequest(restClient).RequestURI(endpoint)
		t.Logf("%s should not be accessible.", req.URL().String())
		_, err := req.Do(ctx).Raw()
		require.Error(t, err)
	}
}
