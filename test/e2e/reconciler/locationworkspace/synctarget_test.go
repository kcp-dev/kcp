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

package locationworkspace

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/config/helpers"
	kube124 "github.com/kcp-dev/kcp/config/rootcompute/kube-1.24"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var testFiles embed.FS

func TestSyncTargetExport(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgPath, _ := framework.NewOrganizationFixture(t, source, framework.TODO_WithoutMultiShardSupport())

	schemaPath, schemaWorkspace := framework.NewWorkspaceFixture(t, source, orgPath, framework.TODO_WithoutMultiShardSupport())
	schemaClusterName := logicalcluster.Name(schemaWorkspace.Spec.Cluster)

	computePath, computeWorkspace := framework.NewWorkspaceFixture(t, source, orgPath, framework.TODO_WithoutMultiShardSupport())
	computeClusterName := logicalcluster.Name(computeWorkspace.Spec.Cluster)

	kcpClients, err := kcpclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := kcpdynamic.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	t.Logf("Install today service APIResourceSchema into schema workspace %q", schemaPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(schemaPath).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(schemaPath), mapper, sets.NewString("root-compute-workspace"), "apiresourceschema_services.yaml", kube124.KubeComputeFS)
	require.NoError(t, err)
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(schemaPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "services",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"v124.services.core", "today.cowboys.wildwest.dev"},
		},
	}
	_, err = kcpClients.Cluster(schemaPath).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	syncTargetName := "synctarget"
	t.Logf("Creating a SyncTarget and syncer in %s", computePath)
	syncTarget := framework.NewSyncerFixture(t, source, computePath,
		framework.WithAPIExports(fmt.Sprintf("%s:%s", schemaPath.String(), cowboysAPIExport.Name)),
		framework.WithSyncTargetName(syncTargetName),
	).Start(t)

	require.Eventually(t, func() bool {
		syncTarget, err := kcpClients.Cluster(computePath).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
		if err != nil {
			return false
		}

		if len(syncTarget.Status.SyncedResources) != 2 {
			return false
		}

		if syncTarget.Status.SyncedResources[1].Resource != "services" ||
			syncTarget.Status.SyncedResources[1].State != workloadv1alpha1.ResourceSchemaAcceptedState {
			return false
		}

		if syncTarget.Status.SyncedResources[0].Resource != "cowboys" ||
			syncTarget.Status.SyncedResources[0].State != workloadv1alpha1.ResourceSchemaIncompatibleState {
			return false
		}

		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	// create virtual workspace rest configs
	rawConfig, err := source.RawConfig()
	require.NoError(t, err)
	virtualWorkspaceRawConfig := rawConfig.DeepCopy()
	virtualWorkspaceRawConfig.Clusters["syncvervw"] = rawConfig.Clusters["base"].DeepCopy()
	virtualWorkspaceRawConfig.Clusters["syncvervw"].Server = rawConfig.Clusters["base"].Server + "/services/syncer/" + computeClusterName.String() + "/" + syncTargetName + "/" + syncTarget.SyncerConfig.SyncTargetUID
	virtualWorkspaceRawConfig.Contexts["syncvervw"] = rawConfig.Contexts["base"].DeepCopy()
	virtualWorkspaceRawConfig.Contexts["syncvervw"].Cluster = "syncvervw"
	virtualWorkspaceConfig, err := clientcmd.NewNonInteractiveClientConfig(*virtualWorkspaceRawConfig, "syncvervw", nil, nil).ClientConfig()
	require.NoError(t, err)
	virtualWorkspaceConfig = rest.AddUserAgent(rest.CopyConfig(virtualWorkspaceConfig), t.Name())

	virtualWorkspaceiscoverClusterClient, err := kcpdiscovery.NewForConfig(virtualWorkspaceConfig)
	require.NoError(t, err)

	framework.Eventually(t, func() (bool, string) {
		_, existingAPIResourceLists, err := virtualWorkspaceiscoverClusterClient.ServerGroupsAndResources()
		if err != nil {
			return false, err.Error()
		}

		// requiredAPIResourceList includes all core APIs plus services API, cowboy API should not be included since it is
		// not compatible to the synctarget.

		diff := cmp.Diff([]*metav1.APIResourceList{requiredAPIResourceListWithService(computeClusterName, schemaClusterName)}, sortAPIResourceList(existingAPIResourceLists))
		return len(diff) == 0, diff
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Synctarget should be authorized to access downstream clusters")
	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClients.Cluster(computePath).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
	}, framework.Is(workloadv1alpha1.SyncerAuthorized))
}

func sortAPIResourceList(list []*metav1.APIResourceList) []*metav1.APIResourceList {
	sort.Sort(ByGroupVersion(list))
	for _, resource := range list {
		sort.Sort(ByName(resource.APIResources))
	}
	return list
}

type ByGroupVersion []*metav1.APIResourceList

func (a ByGroupVersion) Len() int           { return len(a) }
func (a ByGroupVersion) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByGroupVersion) Less(i, j int) bool { return a[i].GroupVersion < a[j].GroupVersion }

type ByName []metav1.APIResource

func (a ByName) Len() int           { return len(a) }
func (a ByName) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByName) Less(i, j int) bool { return a[i].Name < a[j].Name }

func requiredAPIResourceListWithService(computeClusterName, serviceClusterName logicalcluster.Name) *metav1.APIResourceList {
	return &metav1.APIResourceList{
		TypeMeta: metav1.TypeMeta{
			Kind: "APIResourceList",
		},
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{
				Kind:               "ConfigMap",
				Name:               "configmaps",
				SingularName:       "configmap",
				Namespaced:         true,
				Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(computeClusterName, "", "v1", "ConfigMap"),
			},
			{
				Kind:               "Namespace",
				Name:               "namespaces",
				SingularName:       "namespace",
				Namespaced:         false,
				Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(computeClusterName, "", "v1", "Namespace"),
			},
			{
				Kind:               "Namespace",
				Name:               "namespaces/status",
				SingularName:       "",
				Namespaced:         false,
				Verbs:              metav1.Verbs{"get", "patch", "update"},
				StorageVersionHash: "",
			},
			{
				Kind:               "Secret",
				Name:               "secrets",
				SingularName:       "secret",
				Namespaced:         true,
				Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(computeClusterName, "", "v1", "Secret"),
			},
			{
				Kind:               "ServiceAccount",
				Name:               "serviceaccounts",
				SingularName:       "serviceaccount",
				Namespaced:         true,
				Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(computeClusterName, "", "v1", "ServiceAccount"),
			},
			{
				Kind:               "Service",
				Name:               "services",
				SingularName:       "service",
				ShortNames:         []string{"svc"},
				Categories:         []string{"all"},
				Namespaced:         true,
				Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
				StorageVersionHash: discovery.StorageVersionHash(serviceClusterName, "", "v1", "Service"),
			},
			{
				Kind:               "Service",
				Name:               "services/status",
				SingularName:       "",
				Namespaced:         true,
				Verbs:              metav1.Verbs{"get", "patch", "update"},
				StorageVersionHash: "",
			},
		},
	}
}
