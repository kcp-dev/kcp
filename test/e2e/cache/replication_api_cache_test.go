/*
Copyright 2025 The kcp Authors.

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

package cache

import (
	"context"
	"embed"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	crdhelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	kcpapiextensionsv1client "github.com/kcp-dev/client-go/apiextensions/client/typed/apiextensions/v1"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	cache2e "github.com/kcp-dev/kcp/test/e2e/reconciler/cache"
)

//go:embed assets/*.yaml
var testFiles embed.FS

func TestCacheServerReplicationAPI(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	testCases := make([]struct {
		name string
	}, 10)
	for i := range testCases {
		testCases[i].name = fmt.Sprintf("test-case-%d", i)
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := server.BaseConfig(t)
			cacheClientRT := cache2e.ClientRoundTrippersFor(cfg)
			cacheClientRT.UserAgent = "kcp-cache-test"
			rootOrg, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())

			orgProviderKCPClient, err := kcpclientset.NewForConfig(cfg)
			require.NoError(t, err, "error creating cowboys provider kcp client")

			dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
			require.NoError(t, err, "error creating dynamic cluster client")

			extensionsClusterClient, err := kcpapiextensionsv1client.NewForConfig(cfg)
			require.NoError(t, err, "error creating apiextensions cluster client")

			require.NoError(t, err, "error creating kcp cache client")

			t.Logf("Install a instances object CRD into workspace %q", rootOrg)
			mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(orgProviderKCPClient.Cluster(rootOrg).Discovery()))

			err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(rootOrg), mapper, nil, "assets/crd-instances.yaml", testFiles)
			require.NoError(t, err)

			t.Logf("Waiting for CRD to be established")
			name := "instances.machines.svm.io"
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				crd, err := extensionsClusterClient.Cluster(rootOrg).CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
				require.NoError(t, err)
				return crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established), yamlMarshal(t, crd)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CRD to be established")

			mapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(orgProviderKCPClient.Cluster(rootOrg).Discovery()))

			t.Logf("Create a instances object in workspace %q", rootOrg)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(rootOrg), mapper, nil, "assets/instances.yaml", testFiles)
				return err == nil, ""
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for instances object to be created")

			t.Logf("Create a publishedResource in workspace %q", rootOrg)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(rootOrg), mapper, nil, "assets/published-resource-instances.yaml", testFiles)
				return err == nil, ""
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for publishedResource to be created")

			t.Logf("Wait for replication api to be ready and reporting 7 replicas")
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				publishedResource, err := orgProviderKCPClient.Cluster(rootOrg).CacheV1alpha1().CachedResources().Get(ctx, "instances", metav1.GetOptions{})
				require.NoError(t, err)
				return publishedResource != nil && publishedResource.Status.ResourceCounts != nil && publishedResource.Status.ResourceCounts.Cache == 7, yamlMarshal(t, publishedResource)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for replication api to be ready and reporting 7 replicas")

			t.Logf("Delete some resources and wait for the cache to be updated")
			err = dynamicClusterClient.Cluster(rootOrg).Resource(schema.GroupVersionResource{Group: "machines.svm.io", Version: "v1alpha1", Resource: "instances"}).Delete(ctx, "analytics-1", metav1.DeleteOptions{})
			require.NoError(t, err)
			err = dynamicClusterClient.Cluster(rootOrg).Resource(schema.GroupVersionResource{Group: "machines.svm.io", Version: "v1alpha1", Resource: "instances"}).Delete(ctx, "analytics-2", metav1.DeleteOptions{})
			require.NoError(t, err)
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				publishedResource, err := orgProviderKCPClient.Cluster(rootOrg).CacheV1alpha1().CachedResources().Get(ctx, "instances", metav1.GetOptions{})
				require.NoError(t, err)
				return publishedResource != nil && publishedResource.Status.ResourceCounts != nil && publishedResource.Status.ResourceCounts.Cache == 5, yamlMarshal(t, publishedResource)
			}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for replication api to be ready and reporting 5 replicas")
		})
	}
}

func yamlMarshal(t *testing.T, obj interface{}) string {
	data, err := yaml.Marshal(obj)
	require.NoError(t, err)
	return string(data)
}
