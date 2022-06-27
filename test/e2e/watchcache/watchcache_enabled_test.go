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
	"sync"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/informer"
	metadataclient "github.com/kcp-dev/kcp/pkg/metadata"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

const resyncPeriod = 10 * time.Hour
const byWorkspace = "byWorkspace"

func testDDSIF(t *testing.T, cfg *rest.Config, sheriffsGVR schema.GroupVersionResource, expectedClusterName logicalcluster.Name) {
	t.Logf("testing DDSIF for %v in cluster %v", sheriffsGVR.String(), expectedClusterName.String())
	kcpClusterClient, err := kcpclient.NewClusterForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	kcpClient := kcpClusterClient.Cluster(logicalcluster.Wildcard)
	kubeClusterClient, err := kubernetesclientset.NewClusterForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	metadataClusterClient, err := metadataclient.NewDynamicMetadataClusterClientForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	kcpSharedInformerFactory := kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpClient, resyncPeriod)

	ddsif := informer.NewDynamicDiscoverySharedInformerFactory(
		kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces().Lister(),
		kubeClusterClient.DiscoveryClient,
		metadataClusterClient.Cluster(logicalcluster.Wildcard),
		func(obj interface{}) bool { return true }, 60*time.Second,
	)
	if err := ddsif.AddIndexers(cache.Indexers{byWorkspace: indexByWorkspace}); err != nil {
		t.Fatal(err)
	}

	go kcpSharedInformerFactory.Start(context.Background().Done())
	ddsif.Start(context.Background())

	require.Eventually(t, func() bool {
		listers, notSynced := ddsif.Listers()
		for _, ns := range notSynced {
			klog.Infof("db: ddsif not synced %v", ns.String())
		}

		for gvr, lister := range listers {
			obj, err := lister.List(labels.Everything())
			if err != nil {
				klog.Errorf("db: failed to list items for %v due to %v", gvr.String(), err)
			}
			if gvr.String() == sheriffsGVR.String() {
				for _, o := range obj {
					u := o.(*unstructured.Unstructured)
					if u.GetClusterName() == expectedClusterName.String() {
						return true
					}
				}
			}
		}
		return false
	}, wait.ForeverTestTimeout*3, time.Millisecond*100, "ddsif hasn't found newyork.io resource")
}

func indexByWorkspace(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}

	lcluster := logicalcluster.From(metaObj)
	return []string{lcluster.String()}, nil
}

func TestWatchCacheEnabledForCRD(t *testing.T) {
	t.Parallel()
	server := framework.SharedKcpServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	org := framework.NewOrganizationFixture(t, server)
	cluster := framework.NewWorkspaceFixture(t, server, org)
	cfg := server.DefaultConfig(t)
	kubeClusterClient, err := kubernetesclientset.NewClusterForConfig(cfg)
	require.NoError(t, err)

	t.Log("Creating wildwest.dev CRD")
	apiExtensionsClients, err := apiextensionsclient.NewClusterForConfig(cfg)
	require.NoError(t, err)
	crdClient := apiExtensionsClients.Cluster(cluster).ApiextensionsV1().CustomResourceDefinitions()

	t.Log("Creating wildwest.dev.cowboys CR")
	wildwest.Create(t, crdClient, metav1.GroupResource{Group: "wildwest.dev", Resource: "cowboys"})
	wildwestClusterClient, err := wildwestclientset.NewClusterForConfig(cfg)
	require.NoError(t, err)
	_, err = wildwestClusterClient.Cluster(cluster).WildwestV1alpha1().Cowboys("default").Create(ctx, &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "efficientluke",
		},
		Spec: wildwestv1alpha1.CowboySpec{
			Intent: "should be kept in memory",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Getting wildwest.dev.cowboys 10 times from the watch cache")
	for i := 0; i < 10; i++ {
		res, err := wildwestClusterClient.Cluster(cluster).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
		require.NoError(t, err)
		require.Equal(t, 1, len(res.Items), "expected to get exactly one cowboy")
	}

	totalCacheHits, cowboysCacheHit := collectCacheHitsFor(ctx, t, kubeClusterClient, "/wildwest.dev/cowboys/customresources")
	if totalCacheHits == 0 {
		t.Fatalf("the watch cache is turned off, didn't find instances of %q metrics", "apiserver_cache_list_total")
	}
	if cowboysCacheHit < 10 {
		t.Fatalf("expected to get cowboys.wildwest.dev CRD from the cache at least 10 times, got %v", cowboysCacheHit)
	}
}

func TestWatchCacheEnabledForAPIBindings(t *testing.T) {
	t.Parallel()
	server := framework.SharedKcpServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := server.DefaultConfig(t)
	kcpClusterClient, err := kcpclientset.NewClusterForConfig(cfg)
	require.NoError(t, err)
	dynamicClusterClient, err := dynamic.NewClusterForConfig(cfg)
	require.NoError(t, err)
	kubeClusterClient, err := kubernetesclientset.NewClusterForConfig(cfg)
	require.NoError(t, err)

	org := framework.NewOrganizationFixture(t, server)
	wsExport1a := framework.NewWorkspaceFixture(t, server, org)
	wsConsume1a := framework.NewWorkspaceFixture(t, server, org)
	group := "newyork.io"

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, wsExport1a, kcpClusterClient, group, "export1")
	apifixtures.BindToExport(ctx, t, wsExport1a, group, wsConsume1a, kcpClusterClient)
	apifixtures.CreateSheriff(ctx, t, dynamicClusterClient, wsConsume1a, group, wsConsume1a.String())

	sheriffsGVR := schema.GroupVersionResource{Group: group, Resource: "sheriffs", Version: "v1"}

	// test dynamic informer
	dynamicInformerCounterLock := sync.Mutex{}
	var dynamicInformerSeenAdd, dynamicInformerSeenUpdate, dynamicInformerExpectedUpdates int
	inf := dynamicinformer.NewFilteredDynamicInformer(dynamicClusterClient.Cluster(wsConsume1a), sheriffsGVR, "default", 0, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, nil)
	inf.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			t.Logf("dynamic informer, add, got %v", obj)
			dynamicInformerCounterLock.Lock()
			dynamicInformerSeenAdd++
			dynamicInformerCounterLock.Unlock()
		},
		UpdateFunc: func(old, obj interface{}) {
			t.Logf("dynamic informer, update, got %v", obj)
			dynamicInformerCounterLock.Lock()
			dynamicInformerSeenUpdate++
			dynamicInformerCounterLock.Unlock()
		},
		DeleteFunc: func(obj interface{}) {
			t.Logf("dynamic informer, delete, got %v", obj)
		},
	})
	t.Log("starting dynamic informer for sheriffs.newyork.io")
	go inf.Informer().Run(ctx.Done())
	// end of dynamic inf

	t.Log("Updating sheriffs.newyork.io 10 times")
	dynamicInformerExpectedUpdates = 1
	for i := 0; i < 1; i++ {
		res, err := dynamicClusterClient.Cluster(wsConsume1a).Resource(sheriffsGVR).Namespace("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
		require.NoError(t, err)

		sheriff := res.Items[0]
		sheriff.SetAnnotations(map[string]string{fmt.Sprintf("%v", i): "abcd"})

		_, err = dynamicClusterClient.Cluster(wsConsume1a).Resource(sheriffsGVR).Namespace("default").Update(ctx, &sheriff, metav1.UpdateOptions{})
		require.NoError(t, err)
	}

	require.Eventually(t, func() bool {
		dynamicInformerCounterLock.Lock()
		defer dynamicInformerCounterLock.Unlock()
		if dynamicInformerSeenAdd != 1 && dynamicInformerSeenUpdate != dynamicInformerExpectedUpdates {
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "dynamic informer for %v seen %v updates (expected %v) and %v additions (expected %v) ", sheriffsGVR, func() int {
		dynamicInformerCounterLock.Lock()
		defer dynamicInformerCounterLock.Unlock()
		return dynamicInformerSeenUpdate
	}(), dynamicInformerExpectedUpdates,
		func() int {
			dynamicInformerCounterLock.Lock()
			defer dynamicInformerCounterLock.Unlock()
			return dynamicInformerSeenAdd
		}(), 1)

	t.Log("Getting sheriffs.newyork.io 10 times from the watch cache")

	for i := 0; i < 1; i++ {
		res, err := dynamicClusterClient.Cluster(wsConsume1a).Resource(sheriffsGVR).Namespace("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
		require.NoError(t, err)
		require.Equal(t, 1, len(res.Items), "expected to get exactly one sheriff")
	}

	totalCacheHits, sheriffsCacheHit := collectCacheHitsFor(ctx, t, kubeClusterClient, "/newyork.io/sheriffs")
	if totalCacheHits == 0 {
		t.Fatalf("the watch cache is turned off, didn't find instances of %q metrics", "apiserver_cache_list_total")
	}
	if sheriffsCacheHit < 1 {
		t.Fatalf("expected to get sheriffs.newyork.io from the cache at least 10 times, got %v", sheriffsCacheHit)
	}

	testDDSIF(t, cfg, sheriffsGVR, wsConsume1a)
}

func TestWatchCacheEnabledForBuiltinTypes(t *testing.T) {
	t.Parallel()
	server := framework.SharedKcpServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := server.DefaultConfig(t)
	kubeClusterClient, err := kubernetesclientset.NewClusterForConfig(cfg)
	require.NoError(t, err)

	org := framework.NewOrganizationFixture(t, server)
	cluster := framework.NewWorkspaceFixture(t, server, org)
	kubeClient := kubeClusterClient.Cluster(cluster)

	t.Logf("Creating a secret in the default namespace for %q cluster", cluster)
	_, err = kubeClient.CoreV1().Secrets("default").Create(ctx, &v1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "topsecret"}}, metav1.CreateOptions{})
	require.NoError(t, err)

	// since secrets might be common resources to LIST, try to get them an odd number of times
	t.Logf("Getting core.secret 115 times from the watch cache for %q cluster", cluster)
	for i := 0; i < 115; i++ {
		res, err := kubeClient.CoreV1().Secrets("default").List(ctx, metav1.ListOptions{ResourceVersion: "0"})
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
			t.Fatalf("havent't found the %q secret for %q cluster", "topsecret", cluster)
		}
	}

	totalCacheHits, secretsCacheHit := collectCacheHitsFor(ctx, t, kubeClusterClient, "/core/secrets")
	if totalCacheHits == 0 {
		t.Fatalf("the watch cache is turned off, didn't find instances of %q metrics", "apiserver_cache_list_total")
	}
	if secretsCacheHit < 115 {
		t.Fatalf("expected to get core.secrets from the cache at least 115 times, got %v", secretsCacheHit)
	}
}

func collectCacheHitsFor(ctx context.Context, t *testing.T, kubeClusterClient *kubernetesclientset.Cluster, metricResourcePrefix string) (int, int) {
	t.Logf("Reading %q metrics from the API server via %q endpoint for %q prefix", "apiserver_cache_list_total", "/metrics", metricResourcePrefix)
	rsp := kubeClusterClient.RESTClient().Get().AbsPath("/metrics").Do(ctx)
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
