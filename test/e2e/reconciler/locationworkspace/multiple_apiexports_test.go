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
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestMultipleExports(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgClusterName := framework.NewOrganizationFixture(t, source)
	computeClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName.Path())

	kcpClients, err := kcpclientset.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := kcpdynamic.NewForConfig(source.BaseConfig(t))
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	serviceSchemaClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName.Path())
	t.Logf("Install service APIResourceSchema into service schema workspace %q", serviceSchemaClusterName)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(serviceSchemaClusterName.Path()).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(serviceSchemaClusterName.Path()), mapper, sets.NewString("root-compute-workspace"), "apiresourceschema_services.yaml", kube124.KubeComputeFS)
	require.NoError(t, err)
	t.Logf("Create an APIExport for it")
	serviceAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "services",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"v124.services.core"},
		},
	}
	_, err = kcpClients.Cluster(serviceSchemaClusterName.Path()).ApisV1alpha1().APIExports().Create(ctx, serviceAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	ingressSchemaClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName.Path())
	t.Logf("Install ingress APIResourceSchema into ingress schema workspace %q", ingressSchemaClusterName)
	mapper = restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(ingressSchemaClusterName.Path()).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(ingressSchemaClusterName.Path()), mapper, sets.NewString("root-compute-workspace"), "apiresourceschema_ingresses.networking.k8s.io.yaml", kube124.KubeComputeFS)
	require.NoError(t, err)
	t.Logf("Create an APIExport for it")
	ingressAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ingresses",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"v124.ingresses.networking.k8s.io"},
		},
	}
	_, err = kcpClients.Cluster(ingressSchemaClusterName.Path()).ApisV1alpha1().APIExports().Create(ctx, ingressAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	syncTargetName := "synctarget"
	t.Logf("Creating a SyncTarget and syncer in %s", computeClusterName)
	syncTarget := framework.NewSyncerFixture(t, source, computeClusterName,
		framework.WithAPIExports(fmt.Sprintf("%s:%s", serviceSchemaClusterName.String(), serviceAPIExport.Name)),
		framework.WithSyncTargetName(syncTargetName),
		framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
			if !isFakePCluster {
				// Only need to install services
				return
			}
			sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
			require.NoError(t, err, "failed to create apiextensions client")
			t.Logf("Installing test CRDs into sink cluster...")
			kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: "networking.k8s.io", Resource: "ingresses"},
			)
			require.NoError(t, err)
		}),
	).Start(t)

	t.Logf("syncTarget should have one resource to sync")
	require.Eventually(t, func() bool {
		syncTarget, err := kcpClients.Cluster(computeClusterName.Path()).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
		if err != nil {
			return false
		}

		if len(syncTarget.Status.SyncedResources) != 1 {
			return false
		}

		if syncTarget.Status.SyncedResources[0].Resource != "services" ||
			syncTarget.Status.SyncedResources[0].State != workloadv1alpha1.ResourceSchemaAcceptedState {
			return false
		}

		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Synctarget should be authorized to access downstream clusters")
	framework.Eventually(t, func() (bool, string) {
		syncTarget, err := kcpClients.Cluster(computeClusterName.Path()).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		done := conditions.IsTrue(syncTarget, workloadv1alpha1.SyncerAuthorized)
		var reason string
		if !done {
			condition := conditions.Get(syncTarget, workloadv1alpha1.SyncerAuthorized)
			if condition != nil {
				reason = fmt.Sprintf("Not done waiting for SyncTarget to be authorized: %s: %s", condition.Reason, condition.Message)
			} else {
				reason = "Not done waiting for SyncTarget to be authorized: no condition present"
			}
		}
		return done, reason
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Patch synctarget with new export")
	patchData := fmt.Sprintf(
		`{"spec":{"supportedAPIExports":[{"path":%q,"exportName":"services"},{"path":%q,"exportName":"ingresses"}]}}`, serviceSchemaClusterName.String(), ingressSchemaClusterName.String())
	_, err = kcpClients.Cluster(computeClusterName.Path()).WorkloadV1alpha1().SyncTargets().Patch(ctx, syncTargetName, types.MergePatchType, []byte(patchData), metav1.PatchOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		syncTarget, err := kcpClients.Cluster(computeClusterName.Path()).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
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

		if syncTarget.Status.SyncedResources[0].Resource != "ingresses" ||
			syncTarget.Status.SyncedResources[0].State != workloadv1alpha1.ResourceSchemaAcceptedState {
			return false
		}

		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Synctarget should not be authorized to access downstream clusters")
	require.Eventually(t, func() bool {
		syncTarget, err := kcpClients.Cluster(computeClusterName.Path()).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
		if err != nil {
			return false
		}

		return conditions.IsFalse(syncTarget, workloadv1alpha1.SyncerAuthorized)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Update clusterole so syncer can start to sync")
	downstreamKubeClient := syncTarget.DownstreamKubeClient
	require.Eventually(t, func() bool {
		clusterRole, err := downstreamKubeClient.RbacV1().ClusterRoles().Get(ctx, syncTarget.SyncerID, metav1.GetOptions{})
		if err != nil {
			return false
		}

		clusterRole.Rules = append(clusterRole.Rules, rbacv1.PolicyRule{
			APIGroups: []string{"networking.k8s.io"},
			Resources: []string{"ingresses"},
			Verbs:     []string{"*"},
		})

		_, err = downstreamKubeClient.RbacV1().ClusterRoles().Update(ctx, clusterRole, metav1.UpdateOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Synctarget should be authorized to access downstream clusters")
	framework.Eventually(t, func() (bool, string) {
		syncTarget, err := kcpClients.Cluster(computeClusterName.Path()).WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		done := conditions.IsTrue(syncTarget, workloadv1alpha1.SyncerAuthorized)
		var reason string
		if !done {
			condition := conditions.Get(syncTarget, workloadv1alpha1.SyncerAuthorized)
			if condition != nil {
				reason = fmt.Sprintf("Not done waiting for SyncTarget to be authorized: %s: %s", condition.Reason, condition.Message)
			} else {
				reason = "Not done waiting for SyncTarget to be authorized: no condition present"
			}
		}
		return done, reason
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
	require.Eventually(t, func() bool {
		_, existingAPIResourceLists, err := virtualWorkspaceiscoverClusterClient.ServerGroupsAndResources()

		if err != nil {
			return false
		}
		requiredIngressAPIResourceList := &metav1.APIResourceList{
			TypeMeta: metav1.TypeMeta{
				Kind:       "APIResourceList",
				APIVersion: "v1",
			},
			GroupVersion: "networking.k8s.io/v1",
			APIResources: []metav1.APIResource{
				{
					Kind:               "Ingress",
					Name:               "ingresses",
					SingularName:       "ingress",
					ShortNames:         []string{"ing"},
					Namespaced:         true,
					Verbs:              metav1.Verbs{"get", "list", "patch", "update", "watch"},
					StorageVersionHash: discovery.StorageVersionHash(ingressSchemaClusterName, "networking.k8s.io", "v1", "Ingress"),
				},
				{
					Kind:               "Ingress",
					Name:               "ingresses/status",
					SingularName:       "",
					Namespaced:         true,
					Verbs:              metav1.Verbs{"get", "patch", "update"},
					StorageVersionHash: "",
				},
			},
		}

		return len(cmp.Diff([]*metav1.APIResourceList{
			requiredIngressAPIResourceList, requiredAPIResourceListWithService(computeClusterName, serviceSchemaClusterName)}, sortAPIResourceList(existingAPIResourceLists))) == 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

}
