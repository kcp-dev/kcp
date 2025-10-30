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

package watchcache

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWatchCacheEnabledForCRD(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	// note that we schedule the workspace on the root shard because
	// we need a direct and privileged access to it for downloading the metrics
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithRootShard())
	clusterConfig := server.BaseConfig(t)
	cowBoysGR := metav1.GroupResource{Group: "wildwest.dev", Resource: "cowboys"}

	t.Log("Creating wildwest.dev CRD")
	kcpClusterClient, err := kcpapiextensionsclientset.NewForConfig(clusterConfig)
	require.NoError(t, err)
	kcpCRDClusterClient := kcpClusterClient.ApiextensionsV1().CustomResourceDefinitions()

	t.Log("Creating wildwest.dev.cowboys CR")
	wildwest.Create(t, wsPath, kcpCRDClusterClient, cowBoysGR)
	wildwestClusterClient, err := wildwestclientset.NewForConfig(clusterConfig)
	require.NoError(t, err)
	_, err = wildwestClusterClient.Cluster(wsPath).WildwestV1alpha1().Cowboys("default").Create(ctx, &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "efficientluke",
		},
		Spec: wildwestv1alpha1.CowboySpec{
			Intent: "should be kept in memory",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Logf("Waiting until the watch cache is primed for %v for cluster %v", cowBoysGR, wsPath)
	assertWatchCacheIsPrimed(t, func() error {
		res, err := wildwestClusterClient.Cluster(wsPath).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
		if err != nil {
			return err
		}
		if len(res.Items) == 0 {
			return fmt.Errorf("cache hasn't been primed for %v yet", cowBoysGR)
		}
		return nil
	})

	t.Log("Getting wildwest.dev.cowboys 10 times from the watch cache")
	for range 10 {
		res, err := wildwestClusterClient.Cluster(wsPath).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
		require.NoError(t, err)
		require.Equal(t, 1, len(res.Items), "expected to get exactly one cowboy")
	}

	totalCacheHits, cowboysCacheHit := collectCacheHitsFor(ctx, t, server.RootShardSystemMasterBaseConfig(t), "/wildwest.dev/cowboys/customresources")
	if totalCacheHits == 0 {
		t.Fatalf("the watch cache is turned off, didn't find instances of %q metrics", "apiserver_cache_list_total")
	}
	if cowboysCacheHit < 10 {
		t.Fatalf("expected to get cowboys.wildwest.dev CRD from the cache at least 10 times, got %v", cowboysCacheHit)
	}
}

func TestWatchCacheEnabledForAPIBindings(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clusterConfig := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(clusterConfig)
	require.NoError(t, err)
	dynamicKcpClusterClient, err := kcpdynamic.NewForConfig(clusterConfig)
	require.NoError(t, err)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	// note that we schedule the workspaces on the root shard because
	// we need a direct and privileged access to it for downloading the metrics
	wsExport1aPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithRootShard())
	wsConsume1aPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithRootShard())
	group := "newyork.io"

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, wsExport1aPath, kcpClusterClient, group, "export1")
	apifixtures.BindToExport(ctx, t, wsExport1aPath, group, wsConsume1aPath, kcpClusterClient)
	createSheriff(ctx, t, dynamicKcpClusterClient, wsConsume1aPath, group, wsConsume1aPath.String())

	sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}
	t.Logf("Waiting until the watch cache is primed for %v for cluster %v", sheriffsGVR, wsConsume1aPath)
	assertWatchCacheIsPrimed(t, func() error {
		res, err := dynamicKcpClusterClient.Cluster(wsConsume1aPath).Resource(sheriffsGVR).Namespace("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
		if err != nil {
			return err
		}
		if len(res.Items) == 0 {
			return fmt.Errorf("cache hasn't been primed for %v yet", sheriffsGVR)
		}
		return nil
	})
	t.Log("Getting sheriffs.newyork.io 10 times from the watch cache")
	for range 10 {
		res, err := dynamicKcpClusterClient.Cluster(wsConsume1aPath).Resource(sheriffsGVR).Namespace("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
		require.NoError(t, err)
		require.Equal(t, 1, len(res.Items), "expected to get exactly one sheriff")
	}

	totalCacheHits, sheriffsCacheHit := collectCacheHitsFor(ctx, t, server.RootShardSystemMasterBaseConfig(t), "/newyork.io/sheriffs")
	if totalCacheHits == 0 {
		t.Fatalf("the watch cache is turned off, didn't find instances of %q metrics", "apiserver_cache_list_total")
	}
	if sheriffsCacheHit < 10 {
		t.Fatalf("expected to get sheriffs.newyork.io from the cache at least 10 times, got %v", sheriffsCacheHit)
	}
}

func TestWatchCacheEnabledForBuiltinTypes(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clusterConfig := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(clusterConfig)
	require.NoError(t, err)
	secretsGR := metav1.GroupResource{Group: "", Resource: "secrets"}

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	// note that we schedule the workspace on the root shard because
	// we need a direct and privileged access to it for downloading the metrics
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithRootShard())

	t.Logf("Creating a secret in the default namespace for %q cluster", wsPath)
	_, err = kubeClusterClient.Cluster(wsPath).CoreV1().Secrets("default").Create(ctx, &v1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "topsecret"}}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Logf("Waiting until the watch cache is primed for %v for cluster %v", secretsGR, wsPath)
	assertWatchCacheIsPrimed(t, func() error {
		res, err := kubeClusterClient.Cluster(wsPath).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
		if err != nil {
			return err
		}
		if len(res.Items) == 0 {
			return fmt.Errorf("cache hasn't been primed for %v yet", secretsGR)
		}
		return nil
	})

	// since secrets might be common resources to LIST, try to get them an odd number of times
	t.Logf("Getting core.secret 115 times from the watch cache for %q cluster", wsPath)
	for range 115 {
		res, err := kubeClusterClient.Cluster(wsPath).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(res.Items), 1, "expected to get at least one secret")

		found := false
		for _, secret := range res.Items {
			if secret.Name == "topsecret" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("havent't found the %q secret for %q cluster", "topsecret", wsPath)
		}
	}

	totalCacheHits, secretsCacheHit := collectCacheHitsFor(ctx, t, server.RootShardSystemMasterBaseConfig(t), "/core/secrets")
	if totalCacheHits == 0 {
		t.Fatalf("the watch cache is turned off, didn't find instances of %q metrics", "apiserver_cache_list_total")
	}
	if secretsCacheHit < 115 {
		t.Fatalf("expected to get core.secrets from the cache at least 115 times, got %v", secretsCacheHit)
	}
}

func collectCacheHitsFor(ctx context.Context, t *testing.T, rootCfg *rest.Config, metricResourcePrefix string) (int, int) {
	t.Helper()

	rootShardKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(rootCfg)
	require.NoError(t, err)

	t.Logf("Reading %q metrics from the API server via %q endpoint for %q prefix", "apiserver_cache_list_total", "/metrics", metricResourcePrefix)
	rsp := rootShardKubeClusterClient.RESTClient().Get().AbsPath("/metrics").Do(ctx)
	raw, err := rsp.Raw()
	require.NoError(t, err)
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	var totalCacheHits, prefixCacheHit int
	for scanner.Scan() {
		txt := scanner.Text()
		if strings.Contains(txt, "apiserver_cache_list_total") {
			totalCacheHits++
			if strings.Contains(txt, fmt.Sprintf(`resource_prefix="%v`, metricResourcePrefix)) {
				re := regexp.MustCompile(`\b\d+\b`)
				prefixCacheHitInstance, err := strconv.Atoi(string(re.Find([]byte(txt))))
				if err != nil {
					t.Fatalf("unable to extract the number of cache hits from %v", txt)
				}
				prefixCacheHit += prefixCacheHitInstance
			}
		}
	}
	return totalCacheHits, prefixCacheHit
}

func assertWatchCacheIsPrimed(t *testing.T, fn func() error) {
	t.Helper()
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		if err := fn(); err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*200, "the watch cache hasn't been primed in the allotted time")
}

func createSheriff(
	ctx context.Context,
	t *testing.T,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	clusterName logicalcluster.Path,
	group, name string,
) {
	t.Helper()

	name = strings.ReplaceAll(name, ":", "-")

	t.Logf("Creating %s/v1 sheriffs %s|default/%s", group, clusterName, name)

	sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}

	sheriff := &unstructured.Unstructured{}
	sheriff.SetAPIVersion(group + "/v1")
	sheriff.SetKind("Sheriff")
	sheriff.SetName(name)

	// CRDs are asynchronously served because they are informer based.
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		if _, err := dynamicClusterClient.Cluster(clusterName).Resource(sheriffsGVR).Namespace("default").Create(ctx, sheriff, metav1.CreateOptions{}); err != nil {
			return false, fmt.Sprintf("failed to create Sheriff %s|%s: %v", clusterName, name, err.Error())
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "error creating Sheriff %s|%s", clusterName, name)
}
