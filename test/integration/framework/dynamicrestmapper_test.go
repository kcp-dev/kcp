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

package framework

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/martinlindhe/base36"
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
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/integration/framework/assets"
)

func TestDynamicRestMapper(t *testing.T) {
	ctx := context.Background()

	server, kcpClientSet, _ := StartTestServer(t)

	name := "test-workspace-" + strings.ToLower(base36.Encode(rand.Uint64())[:8])

	cfg, err := server.ClientConfig.ClientConfig()
	require.NoError(t, err, "error creating client config")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "error creating dynamic cluster client")

	extensionsClusterClient, err := kcpapiextensionsv1client.NewForConfig(cfg)
	require.NoError(t, err, "error creating apiextensions cluster client")

	workspace := kcptesting.NewLowLevelWorkspaceFixture(t, kcpClientSet, kcpClientSet, core.RootCluster.Path(), kcptesting.WithName(name))

	logicalClusterName := logicalcluster.Name(workspace.Spec.Cluster)

	drm := server.Server.DynRESTMapper.ForCluster(logicalClusterName)

	t.Logf("Testing if we can resolve the ConfigMap kind")
	var gvk schema.GroupVersionKind
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		gvk, err = drm.KindFor(schema.GroupVersionResource{
			Group:    "",
			Version:  "v1",
			Resource: "configmaps",
		})
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CRD to be established")
	require.NoError(t, err)

	require.Equal(t, "ConfigMap", gvk.Kind)

	t.Logf("Testing if we can resolve the KubeCluster kind")

	t.Logf("Install a mount object CRD into workspace %q", workspace.Name)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClientSet.Cluster(logicalClusterName.Path()).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(logicalClusterName.Path()), mapper, nil, "crd_kubecluster.yaml", assets.EmbeddedResources)
	require.NoError(t, err)

	t.Logf("Waiting for CRD to be established")
	crdName := "kubeclusters.contrib.kcp.io"
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		crd, err := extensionsClusterClient.Cluster(logicalClusterName.Path()).CustomResourceDefinitions().Get(ctx, crdName, metav1.GetOptions{})
		require.NoError(t, err)
		return crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established), yamlMarshal(t, crd)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CRD to be established")

	t.Logf("Testing if we can resolve the KubeCluster kind")

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		gvk, err = drm.KindFor(schema.GroupVersionResource{
			Group:    "contrib.kcp.io",
			Version:  "v1alpha1",
			Resource: "kubeclusters",
		})
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CRD to be established")

	require.NoError(t, err)
	require.Equal(t, "KubeCluster", gvk.Kind)
}

func yamlMarshal(t *testing.T, obj interface{}) string {
	data, err := yaml.Marshal(obj)
	require.NoError(t, err)
	return string(data)
}
