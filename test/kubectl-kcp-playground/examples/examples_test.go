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
	"os"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/reconciler/deployment/workloads"
	"github.com/kcp-dev/kcp/test/kubectl-kcp-playground/plugin"
)

func TestCowboyApiExample(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "kcp-playground")

	// Move to the project root, because all the path in example files are relative to this folder
	chDirToProjectRoot(t)

	t.Log("create a playground using the cowboy-api config file")
	playground := createPlayground(t, "test/kubectl-kcp-playground/examples/apis/cowboy-api.yaml")

	// Validate the playground
	t.Log("test it is possible to create an object of type cowboys in the applications workspace")
	createCowboy(t, playground)
}

func TestCowboyApiToPClusterExample(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "kcp-playground")

	// Move to the project root, because all the path in example files are relative to this folder
	chDirToProjectRoot(t)

	t.Log("create a playground using the cowboy-api to pCluster config file")
	playground := createPlayground(t, "test/kubectl-kcp-playground/examples/apis/cowboy-api-to-pcluster.yaml")

	// Validate the playground
	t.Log("test it is possible to create an object of type cowboys in the applications workspace")
	createCowboy(t, playground)

	t.Log("check the object has been synced to the pCluster")
	config, err := playground.PClusters["us-east1"].RawConfig()
	require.NoError(t, err)
	restConfig, err := clientcmd.NewDefaultClientConfig(config, nil).ClientConfig()
	require.NoError(t, err)
	downstreamDynamic, err := dynamic.NewForConfig(restConfig)

	require.Eventually(t, func() bool {
		list, err := downstreamDynamic.Resource(schema.GroupVersionResource{
			Group:    "wildwest.dev",
			Version:  "v1alpha1",
			Resource: "cowboys",
		}).List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err, "error listing cowboys inside %q", playground.PClusters["us-east1"].Name())
		return len(list.Items) == 1
	}, 10*time.Second, 500*time.Millisecond, "expected 1 cowboys inside %q", playground.PClusters["us-east1"].Name())
}

func TestEastWestPlacementExample(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "kcp-playground")

	// Move to the project root, because all the path in example files are relative to this folder
	chDirToProjectRoot(t)

	t.Log("create a playground using the east-west placement config file")
	playground := createPlayground(t, "test/kubectl-kcp-playground/examples/placement/east-west.yaml")

	// Validate the playground
	t.Log("test it is possible to create a deployment in the applications workspace")
	wsPath := logicalcluster.NewPath("root:my-org:applications")

	raw, err := workloads.FS.ReadFile("deployment.yaml")
	require.NoError(t, err)
	kubeconfigPath := playground.WriteWorkspaceKubeConfig(t, playground.Shards[plugin.MainShardName], wsPath)
	framework.KubectlApply(t, kubeconfigPath, raw)

	t.Log("check the object has been synced to both the pCluster")
	eastConfig, err := playground.PClusters["us-east1"].RawConfig()
	require.NoError(t, err)
	eastRestConfig, err := clientcmd.NewDefaultClientConfig(eastConfig, nil).ClientConfig()
	require.NoError(t, err)
	eastDownstreamDynamic, err := dynamic.NewForConfig(eastRestConfig)

	westConfig, err := playground.PClusters["us-west1"].RawConfig()
	require.NoError(t, err)
	westRestConfig, err := clientcmd.NewDefaultClientConfig(westConfig, nil).ClientConfig()
	require.NoError(t, err)
	westDownstreamDynamic, err := dynamic.NewForConfig(westRestConfig)

	require.Eventually(t, func() bool {
		eastList, err := eastDownstreamDynamic.Resource(schema.GroupVersionResource{
			Group:    "apps",
			Version:  "v1",
			Resource: "deployments",
		}).List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err, "error listing cowboys inside %q", playground.PClusters["us-east1"].Name())

		isInEast := false
		for _, d := range eastList.Items {
			if d.GetName() == "test" {
				isInEast = true
				break
			}
		}

		westList, err := westDownstreamDynamic.Resource(schema.GroupVersionResource{
			Group:    "apps",
			Version:  "v1",
			Resource: "deployments",
		}).List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err, "error listing cowboys inside %q", playground.PClusters["us-west1"].Name())

		isInWest := false
		for _, d := range westList.Items {
			if d.GetName() == "test" {
				isInWest = true
				break
			}
		}

		return isInEast && isInWest
	}, 15*time.Second, 1*time.Second, "expected 1 deployment inside both %q and %q", playground.PClusters["us-east1"].Name(), playground.PClusters["us-west1"].Name())
}

func TestPoolOfClusterPlacementExample(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "kcp-playground")

	// Move to the project root, because all the path in example files are relative to this folder
	chDirToProjectRoot(t)

	t.Log("create a playground using the pool of cluster placement config file")
	playground := createPlayground(t, "test/kubectl-kcp-playground/examples/placement/pool-of-clusters.yaml")

	// Validate the playground
	t.Log("test it is possible to create a deployment in the applications workspace")
	wsPath := logicalcluster.NewPath("root:my-org:applications")

	raw, err := workloads.FS.ReadFile("deployment.yaml")
	require.NoError(t, err)
	kubeconfigPath := playground.WriteWorkspaceKubeConfig(t, playground.Shards[plugin.MainShardName], wsPath)
	framework.KubectlApply(t, kubeconfigPath, raw)

	t.Log("check the object has been synced to both the pCluster")
	east1Config, err := playground.PClusters["us-east1-cluster1"].RawConfig()
	require.NoError(t, err)
	east1RestConfig, err := clientcmd.NewDefaultClientConfig(east1Config, nil).ClientConfig()
	require.NoError(t, err)
	east1DownstreamDynamic, err := dynamic.NewForConfig(east1RestConfig)

	east2Config, err := playground.PClusters["us-east1-cluster2"].RawConfig()
	require.NoError(t, err)
	east2RestConfig, err := clientcmd.NewDefaultClientConfig(east2Config, nil).ClientConfig()
	require.NoError(t, err)
	east2DownstreamDynamic, err := dynamic.NewForConfig(east2RestConfig)

	require.Eventually(t, func() bool {
		east1List, err := east1DownstreamDynamic.Resource(schema.GroupVersionResource{
			Group:    "apps",
			Version:  "v1",
			Resource: "deployments",
		}).List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err, "error listing cowboys inside %q", playground.PClusters["us-east1-cluster1"].Name())

		isInEast1 := false
		for _, d := range east1List.Items {
			if d.GetName() == "test" {
				isInEast1 = true
				break
			}
		}

		east2List, err := east2DownstreamDynamic.Resource(schema.GroupVersionResource{
			Group:    "apps",
			Version:  "v1",
			Resource: "deployments",
		}).List(context.TODO(), metav1.ListOptions{})
		require.NoError(t, err, "error listing cowboys inside %q", playground.PClusters["us-east1-cluster2"].Name())

		isInEast2 := false
		for _, d := range east2List.Items {
			if d.GetName() == "test" {
				isInEast2 = true
				break
			}
		}

		return isInEast1 != isInEast2
	}, 15*time.Second, 1*time.Second, "expected 1 deployment inside on of %q and %q", playground.PClusters["us-east1-cluster1"].Name(), playground.PClusters["us-east1-cluster2"].Name())
}

func TestKcpPlaygroundConfigDoc(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "kcp-playground")

	// Move to the project root, because all the path in example files are relative to this folder
	chDirToProjectRoot(t)

	// Read config example file linked from the book
	spec := &plugin.PlaygroundSpec{}
	err := spec.CompleteFromFile("docs/content/developers/kcp-playground.yaml")
	require.NoError(t, err, "failed to read config example file linked from the book")
}

func createCowboy(t *testing.T, playground *plugin.StartedPlaygroundFixture) {
	wsPath := logicalcluster.NewPath("root:my-org:applications")
	wildwestClusterClient, err := wildwestclientset.NewForConfig(playground.Shards[plugin.MainShardName].BaseConfig(t))
	require.NoError(t, err)

	cowboyClient := wildwestClusterClient.Cluster(wsPath).WildwestV1alpha1().Cowboys("default")
	cowboys, err := cowboyClient.List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err, "error listing cowboys inside %q", wsPath)
	require.Zero(t, len(cowboys.Items), "expected 0 cowboys inside %q", wsPath)

	cowboy := &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "woody",
			Namespace: "default",
		},
	}
	_, err = cowboyClient.Create(context.TODO(), cowboy, metav1.CreateOptions{})
	require.NoError(t, err, "error creating cowboy in %q", wsPath)
}

func chDirToProjectRoot(t *testing.T) {
	err := os.Chdir("../../..")
	require.NoError(t, err)
}

func createPlayground(t *testing.T, configFile string) *plugin.StartedPlaygroundFixture {
	// read playground config
	spec := &plugin.PlaygroundSpec{}
	err := spec.CompleteFromFile(configFile)
	require.NoError(t, err, "failed to read the test config")

	// start the playground
	playground := plugin.NewPlaygroundFixture(t, spec).Start(t)
	require.Equal(t, len(spec.Shards), len(playground.Shards), "failed to get the desired number of running shards")

	// TODO: consider if to add some "mechanical" validation that all the object defined in the config exists.
	//  (this will add to the playground validation executed by automating the steps described in the doc as implemented in each step).

	return playground
}
