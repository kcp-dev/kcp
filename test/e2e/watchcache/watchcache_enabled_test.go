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

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/core/v1"
	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWatchCacheEnabledForCRD(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	org := framework.NewOrganizationFixture(t, server)
	clusterName := framework.NewWorkspaceFixture(t, server, org.Path(), framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
	rootShardConfig := server.RootShardSystemMasterBaseConfig(t)
	cowBoysGR := metav1.GroupResource{Group: "wildwest.dev", Resource: "cowboys"}

	t.Log("Creating wildwest.dev CRD")
	rootCRDClusterClient, err := kcpapiextensionsclientset.NewForConfig(rootShardConfig)
	require.NoError(t, err)
	rootShardCRDWildcardClient := rootCRDClusterClient.ApiextensionsV1().CustomResourceDefinitions()

	t.Log("Creating wildwest.dev.cowboys CR")
	wildwest.Create(t, clusterName.Path(), rootShardCRDWildcardClient, cowBoysGR)
	wildwestClusterClient, err := wildwestclientset.NewForConfig(rootShardConfig)
	require.NoError(t, err)
	_, err = wildwestClusterClient.Cluster(clusterName.Path()).WildwestV1alpha1().Cowboys("default").Create(ctx, &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "efficientluke",
		},
		Spec: wildwestv1alpha1.CowboySpec{
			Intent: "should be kept in memory",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Logf("Waiting until the watch cache is primed for %v for cluster %v", cowBoysGR, clusterName)
	assertWatchCacheIsPrimed(t, func() error {
		res, err := wildwestClusterClient.Cluster(clusterName.Path()).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
		if err != nil {
			return err
		}
		if len(res.Items) == 0 {
			return fmt.Errorf("cache hasn't been primed for %v yet", cowBoysGR)
		}
		return nil
	})

	t.Log("Getting wildwest.dev.cowboys 10 times from the watch cache")
	for i := 0; i < 10; i++ {
		res, err := wildwestClusterClient.Cluster(clusterName.Path()).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
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

	server := framework.SharedKcpServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rootShardConfig := server.RootShardSystemMasterBaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(rootShardConfig)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(rootShardConfig)
	require.NoError(t, err)

	org := framework.NewOrganizationFixture(t, server)
	wsExport1a := framework.NewWorkspaceFixture(t, server, org.Path(), framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
	wsConsume1a := framework.NewWorkspaceFixture(t, server, org.Path(), framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))
	group := "newyork.io"

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, wsExport1a.Path(), kcpClusterClient, group, "export1")
	apifixtures.BindToExport(ctx, t, wsExport1a.Path(), group, wsConsume1a.Path(), kcpClusterClient)
	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, wsConsume1a.Path(), group, wsConsume1a.String())

	sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}
	t.Logf("Waiting until the watch cache is primed for %v for cluster %v", sheriffsGVR, wsConsume1a)
	assertWatchCacheIsPrimed(t, func() error {
		res, err := dynamicClusterClient.Cluster(wsConsume1a.Path()).Resource(sheriffsGVR).Namespace("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
		if err != nil {
			return err
		}
		if len(res.Items) == 0 {
			return fmt.Errorf("cache hasn't been primed for %v yet", sheriffsGVR)
		}
		return nil
	})
	t.Log("Getting sheriffs.newyork.io 10 times from the watch cache")
	for i := 0; i < 10; i++ {
		res, err := dynamicClusterClient.Cluster(wsConsume1a.Path()).Resource(sheriffsGVR).Namespace("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
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

	server := framework.SharedKcpServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rootShardConfig := server.RootShardSystemMasterBaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(rootShardConfig)
	require.NoError(t, err)
	secretsGR := metav1.GroupResource{Group: "", Resource: "secrets"}

	org := framework.NewOrganizationFixture(t, server)
	clusterName := framework.NewWorkspaceFixture(t, server, org.Path(), framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))

	t.Logf("Creating a secret in the default namespace for %q cluster", clusterName)
	_, err = kubeClusterClient.Cluster(clusterName.Path()).CoreV1().Secrets("default").Create(ctx, &v1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "topsecret"}}, metav1.CreateOptions{})
	require.NoError(t, err)
	t.Logf("Waiting until the watch cache is primed for %v for cluster %v", secretsGR, clusterName)
	assertWatchCacheIsPrimed(t, func() error {
		res, err := kubeClusterClient.Cluster(clusterName.Path()).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
		if err != nil {
			return err
		}
		if len(res.Items) == 0 {
			return fmt.Errorf("cache hasn't been primed for %v yet", secretsGR)
		}
		return nil
	})

	// since secrets might be common resources to LIST, try to get them an odd number of times
	t.Logf("Getting core.secret 115 times from the watch cache for %q cluster", clusterName)
	for i := 0; i < 115; i++ {
		res, err := kubeClusterClient.Cluster(clusterName.Path()).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
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
			t.Fatalf("havent't found the %q secret for %q cluster", "topsecret", clusterName)
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
				var err error
				re := regexp.MustCompile(`\b\d+\b`)
				prefixCacheHit, err = strconv.Atoi(string(re.Find([]byte(txt))))
				if err != nil {
					t.Fatalf("unable to extract the number of cache hits from %v", txt)
				}
			}
		}
	}
	return totalCacheHits, prefixCacheHit
}

func assertWatchCacheIsPrimed(t *testing.T, fn func() error) {
	framework.Eventually(t, func() (success bool, reason string) {
		if err := fn(); err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*200, "the watch cache hasn't been primed in the allotted time")
}
