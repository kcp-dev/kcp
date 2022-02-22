/*
Copyright 2021 The KCP Authors.

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

package cluster

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const (
	pollInterval     = time.Minute
	numSyncerThreads = 2
)

func (c *Controller) reconcile(ctx context.Context, cluster *clusterv1alpha1.Cluster) error {
	klog.Infof("reconciling cluster %q", cluster.Name)

	logicalCluster := cluster.GetClusterName()

	// Get client from kubeconfig
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(cluster.Spec.KubeConfig))
	if err != nil {
		klog.Errorf("invalid kubeconfig: %v", err)
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.InvalidKubeConfigReason, conditionsv1alpha1.ConditionSeverityError, "Error invalid kubeconfig: %v", err.Error())
		return nil // Don't retry.
	}

	if c.apiImporters[cluster.Name] == nil {
		apiImporter, err := c.StartAPIImporter(cfg, cluster.Name, logicalCluster, time.Minute)
		if err != nil {
			klog.Errorf("error starting the API importer: %v", err)
			conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorStartingAPIImporterReason, conditionsv1alpha1.ConditionSeverityError, "Error starting the API importer: %v", err.Error())
			return nil // Don't retry.
		}
		c.apiImporters[cluster.Name] = apiImporter
	}

	if success, err := c.manageSyncer(ctx, cluster, cfg); err != nil {
		return err
	} else if !success {
		return nil // Don't retry
	}

	// Enqueue another check later
	key, err := cache.MetaNamespaceKeyFunc(cluster)
	if err != nil {
		klog.Error(err)
	} else {
		c.queue.AddAfter(key, pollInterval)
	}
	return nil
}

func (c *Controller) manageSyncer(ctx context.Context, cluster *clusterv1alpha1.Cluster, cfg *rest.Config) (bool, error) {
	if c.syncerManager == nil {
		return true, nil // Nothing to do
	}

	logicalCluster := cluster.GetClusterName()

	groupResources := sets.NewString()

	objs, err := c.apiresourceImportIndexer.ByIndex(LocationInLogicalClusterIndexName, GetLocationInLogicalClusterIndexKey(cluster.Name, logicalCluster))
	if err != nil {
		klog.Errorf("error in cluster reconcile: %v", err)
		return false, err
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

	resourcesToPull := sets.NewString(c.resourcesToSync...)
	for _, kcpResource := range c.genericControlPlaneResources {
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

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		klog.Errorf("error creating client: %v", err)
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorCreatingClientReason, conditionsv1alpha1.ConditionSeverityError, "Error creating client: %v", err.Error())
		return false, nil // Don't retry.
	}

	needsUpdate, err := c.syncerManager.needsUpdate(ctx, cluster, client, groupResources)
	if err != nil {
		return false, err
	}

	if klog.V(2).Enabled() {
		klog.V(2).InfoS("Determining if we need to start or update a syncer",
			"synced-resources", cluster.Status.SyncedResources,
			"group-resources", groupResources,
			"equal", sets.NewString(cluster.Status.SyncedResources...).Equal(groupResources),
			"needs-update", needsUpdate,
		)
	}

	if !needsUpdate {
		return true, nil
	}

	klog.V(2).Info("Need to create/update syncer")
	kubeConfig := c.kubeconfig.DeepCopy()

	if updateSucceeded, err := c.syncerManager.update(ctx, cluster, client, groupResources, kubeConfig); err != nil {
		return false, err
	} else if !updateSucceeded {
		return false, nil // Don't retry
	}

	cluster.Status.SyncedResources = groupResources.List()

	checkSucceeded := c.syncerManager.checkHealth(ctx, cluster, client)
	if !checkSucceeded {
		return false, nil // Don't retry
	}

	return true, nil
}

func (c *Controller) cleanup(ctx context.Context, deletedCluster *clusterv1alpha1.Cluster) {
	klog.Infof("cleanup resources for cluster %q", deletedCluster.Name)

	if apiImporter := c.apiImporters[deletedCluster.Name]; apiImporter != nil {
		apiImporter.Stop()
		delete(c.apiImporters, deletedCluster.Name)
	}

	if c.syncerManager != nil {
		c.syncerManager.cleanup(ctx, deletedCluster)
	}
}
