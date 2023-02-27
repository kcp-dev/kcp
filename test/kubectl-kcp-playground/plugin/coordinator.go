/*
Copyright 2023 The KCP Authors.

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

package plugin

import (
	"context"
	"fmt"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func deploymentCoordinator(t *testing.T, shard framework.RunningServer) {
	kcpClusterClient, err := newKCPClusterClient(shard, "root")
	require.NoError(t, err, "failed to get client for '%s'", shard.Name())

	rootKubernetesAPIExport, err := kcpClusterClient.Cluster(logicalcluster.NewPath("root:compute")).ApisV1alpha1().APIExports().Get(context.TODO(), "kubernetes", metav1.GetOptions{})
	require.NoError(t, err, "failed to retrieve Root compute kubernetes APIExport")

	//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
	require.GreaterOrEqual(t, len(rootKubernetesAPIExport.Status.VirtualWorkspaces), 1, "Root compute kubernetes APIExport should contain at least one virtual workspace URL")

	//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
	rootComputeKubernetesURL := rootKubernetesAPIExport.Status.VirtualWorkspaces[0].URL

	rootComputeConfig := rest.CopyConfig(shard.BaseConfig(t))
	rootComputeConfig.Host = rootComputeKubernetesURL
	rootComputeClusterClient, err := kcpkubernetesclientset.NewForConfig(rootComputeConfig)
	require.NoError(t, err)

	framework.Eventually(t, func() (success bool, reason string) {
		t.Logf("Checking deployment access through a list")
		_, err := rootComputeClusterClient.AppsV1().Deployments().List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("deployments should be exposed by the root compute APIExport URL. But listing deployments produced the following error: %v", err)
		}
		return true, "deployments are exposed"
	}, wait.ForeverTestTimeout, time.Millisecond*500, "deployments should be exposed by the root compute APIExport URL")

	t.Logf("Start the Deployment controller")

	artifactDir, _, err := framework.ScratchDirs(t)
	require.NoError(t, err)

	executableName := "deployment-coordinator"
	cmd := append(framework.DirectOrGoRunCommand(executableName),
		"--kubeconfig="+shard.KubeconfigPath(),
		"--context=base",
		"--server="+rootComputeKubernetesURL,
	)

	// TODO: check if there is a way to avoid logs to show up in the console
	deploymentCoordinator := framework.NewAccessory(t, artifactDir, executableName, cmd...)
	err = deploymentCoordinator.Run(t, framework.WithLogStreaming)
	require.NoError(t, err, "failed to start deployment coordinator")
}
