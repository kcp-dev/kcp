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

package syncer

import (
	"context"
	"fmt"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	clusterctl "github.com/kcp-dev/kcp/pkg/reconciler/cluster"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

type syncerManagerImpl interface {
	name() string
	needsUpdate(ctx context.Context, cluster *clusterv1alpha1.Cluster, client *kubernetes.Clientset, groupResources sets.String) (bool, error)
	update(ctx context.Context, cluster *clusterv1alpha1.Cluster, client *kubernetes.Clientset, groupResources sets.String, kubeConfig *clientcmdapi.Config) (bool, error)
	checkHealth(ctx context.Context, cluster *clusterv1alpha1.Cluster, client *kubernetes.Clientset) bool
	cleanup(ctx context.Context, deletedCluster *clusterv1alpha1.Cluster)
}

type syncerManager struct {
	name string

	apiExtensionsClient      apiextensionsclient.Interface
	kcpClient                kcpclient.Interface
	kubeconfig               clientcmdapi.Config
	resourcesToSync          []string
	syncerManagerImpl        syncerManagerImpl
	apiresourceImportIndexer cache.Indexer

	genericControlPlaneResources []schema.GroupVersionResource
}

func (m *syncerManager) Reconcile(ctx context.Context, cluster *clusterv1alpha1.Cluster) error {
	klog.Infof("%s: reconciling cluster %q", m.name, cluster.Name)

	logicalCluster := cluster.GetClusterName()

	groupResources := sets.NewString()

	objs, err := m.apiresourceImportIndexer.ByIndex(
		clusterctl.LocationInLogicalClusterIndexName,
		clusterctl.GetLocationInLogicalClusterIndexKey(cluster.Name, logicalCluster),
	)
	if err != nil {
		klog.Errorf("%s: error in cluster reconcile: %v", m.name, err)
		return err
	}

	for _, obj := range objs {
		apiResourceImport := obj.(*apiresourcev1alpha1.APIResourceImport)
		if apiResourceImport.IsConditionTrue(apiresourcev1alpha1.Compatible) && apiResourceImport.IsConditionTrue(apiresourcev1alpha1.Available) {
			groupResources.Insert(schema.GroupResource{
				Group:    apiResourceImport.Spec.GroupVersion.APIGroup(),
				Resource: apiResourceImport.Spec.Plural,
			}.String())
		}
	}

	resourcesToPull := sets.NewString(m.resourcesToSync...)
	for _, kcpResource := range m.genericControlPlaneResources {
		if !resourcesToPull.Has(kcpResource.GroupResource().String()) && !resourcesToPull.Has(kcpResource.Resource) {
			continue
		}
		groupVersion := apiresourcev1alpha1.GroupVersion{
			Group:   kcpResource.Group,
			Version: kcpResource.Version,
		}
		groupResources.Insert(schema.GroupResource{
			Group:    groupVersion.APIGroup(),
			Resource: kcpResource.Resource,
		}.String())
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(cluster.Spec.KubeConfig))
	if err != nil {
		klog.Errorf("%s: invalid kubeconfig: %v", m.name, err)
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.InvalidKubeConfigReason, conditionsv1alpha1.ConditionSeverityError, "Error invalid kubeconfig: %v", err.Error())
		return nil
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		klog.Errorf("%s: error creating client: %v", m.name, err)
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorCreatingClientReason, conditionsv1alpha1.ConditionSeverityError, "Error creating client: %v", err.Error())
		return nil
	}

	needsUpdate, err := m.syncerManagerImpl.needsUpdate(ctx, cluster, client, groupResources)
	if err != nil {
		return err
	}

	if klog.V(2).Enabled() {
		klog.V(2).InfoS(fmt.Sprintf("%s: Determining if we need to start or update a syncer", m.name),
			"synced-resources", cluster.Status.SyncedResources,
			"group-resources", groupResources,
			"equal", sets.NewString(cluster.Status.SyncedResources...).Equal(groupResources),
			"needs-update", needsUpdate,
		)
	}

	if !needsUpdate {
		return nil
	}

	klog.V(2).Infof("%s: Need to create/update syncer", m.name)
	kubeConfig := m.kubeconfig.DeepCopy()

	if updateSucceeded, err := m.syncerManagerImpl.update(ctx, cluster, client, groupResources, kubeConfig); err != nil {
		return err
	} else if !updateSucceeded {
		return nil
	}

	cluster.Status.SyncedResources = groupResources.List()

	checkSucceeded := m.syncerManagerImpl.checkHealth(ctx, cluster, client)
	if !checkSucceeded {
		return nil
	}

	return nil
}

func (m *syncerManager) Cleanup(ctx context.Context, deletedCluster *clusterv1alpha1.Cluster) {
	klog.Infof("%s: cleanup resources for cluster %q", m.name, deletedCluster.Name)
	m.syncerManagerImpl.cleanup(ctx, deletedCluster)
}
