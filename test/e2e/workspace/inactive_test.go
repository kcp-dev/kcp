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

package workspace

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/pkg/server/filters"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestInactiveLogicalCluster(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx := t.Context()

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	kcpClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kubeClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Log("Get the logicalcluster")
	lc, err := kcpClient.Cluster(orgPath).CoreV1alpha1().LogicalClusters().Get(ctx, "cluster", v1.GetOptions{})
	require.NoError(t, err)

	t.Log("Mark the logicalcluster as inactive")
	lc.Annotations[filters.InactiveAnnotation] = "true"
	lc, err = kcpClient.Cluster(orgPath).CoreV1alpha1().LogicalClusters().Update(ctx, lc, v1.UpdateOptions{})
	require.NoError(t, err)

	t.Log("Verify that normal requests fail")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kubeClient.Cluster(orgPath).CoreV1().Namespaces().List(ctx, v1.ListOptions{})
		if err == nil {
			return false, "expected error when accessing an inactive logical cluster"
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Remove inactive annotation again")
	delete(lc.Annotations, filters.InactiveAnnotation)
	_, err = kcpClient.Cluster(orgPath).CoreV1alpha1().LogicalClusters().Update(ctx, lc, v1.UpdateOptions{})
	require.NoError(t, err)

	t.Log("Verify that normal requests succeed again")
	assert.NoError(t, err, "expected no error when accessing an active logical cluster")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kubeClient.Cluster(orgPath).CoreV1().Namespaces().List(ctx, v1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}
