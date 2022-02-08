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
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/syncer"
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
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		klog.Errorf("error creating client: %v", err)
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorCreatingClientReason, conditionsv1alpha1.ConditionSeverityError, "Error creating client: %v", err.Error())
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

	objs, err := c.apiresourceImportIndexer.ByIndex(LocationInLogicalClusterIndexName, GetLocationInLogicalClusterIndexKey(cluster.Name, logicalCluster))
	if err != nil {
		klog.Errorf("error in cluster reconcile: %v", err)
		return err
	}

	groupResources := sets.NewString()

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

	needsUpdate := false
	switch c.syncerMode {
	case SyncerModePull:
		upToDate, err := isSyncerInstalledAndUpToDate(ctx, client, logicalCluster, c.syncerImage)
		if err != nil {
			klog.Errorf("error checking if syncer needs to be installed: %v", err)
			return err
		}

		if !upToDate && groupResources.Len() > 0 {
			needsUpdate = true
		}
	case SyncerModePush:
		_, running := c.syncerCancelFuncs[cluster.Name]
		if !running || !sets.NewString(cluster.Status.SyncedResources...).Equal(groupResources) {
			needsUpdate = true
		}
	}

	if klog.V(2).Enabled() {
		klog.V(2).InfoS("Determining if we need to start or update a syncer",
			"synced-resources", cluster.Status.SyncedResources,
			"group-resources", groupResources,
			"equal", sets.NewString(cluster.Status.SyncedResources...).Equal(groupResources),
			"needs-update", needsUpdate,
		)
	}
	if needsUpdate {
		klog.V(2).Info("Need to create/update syncer")
		kubeConfig := c.kubeconfig.DeepCopy()

		switch c.syncerMode {
		case SyncerModePush:
			upstream, err := clientcmd.NewNonInteractiveClientConfig(*kubeConfig, "admin", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
			if err != nil {
				klog.Errorf("error getting kcp kubeconfig: %v", err)
				conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorStartingSyncerReason, conditionsv1alpha1.ConditionSeverityError, "Error getting kcp kubeconfig: %v", err.Error())
				return nil // Don't retry.
			}

			downstream, err := clientcmd.RESTConfigFromKubeConfig([]byte(cluster.Spec.KubeConfig))
			if err != nil {
				klog.Errorf("error getting cluster kubeconfig: %v", err)
				conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorStartingSyncerReason, conditionsv1alpha1.ConditionSeverityError, "Error getting cluster kubeconfig: %v", err.Error())
				return nil // Don't retry.
			}

			syncerCtx, syncerCancel := context.WithCancel(ctx)
			if err := syncer.StartSyncer(syncerCtx, upstream, downstream, groupResources, cluster.Name, logicalCluster, numSyncerThreads); err != nil {
				klog.Errorf("error starting syncer in push mode: %v", err)
				conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorStartingSyncerReason, conditionsv1alpha1.ConditionSeverityError, "Error starting syncer in push mode: %v", err.Error())

				syncerCancel()

				return err
			}

			oldSyncerCancel := c.syncerCancelFuncs[cluster.Name]
			c.syncerCancelFuncs[cluster.Name] = syncerCancel
			if oldSyncerCancel != nil {
				oldSyncerCancel()
			}

			klog.Infof("started push mode syncer for cluster %s in logical cluster %s!", cluster.Name, logicalCluster)
			conditions.MarkTrue(cluster, clusterv1alpha1.ClusterReadyCondition)
		case SyncerModePull:
			kubeConfig.CurrentContext = "admin"
			bytes, err := clientcmd.Write(*kubeConfig)
			if err != nil {
				klog.Errorf("error writing kubeconfig for syncer: %v", err)
				conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorInstallingSyncerReason, conditionsv1alpha1.ConditionSeverityError, "Error writing kubeconfig for syncer: %v", err.Error())
				return nil // Don't retry.
			}
			if err := installSyncer(ctx, client, c.syncerImage, string(bytes), cluster.Name, logicalCluster, groupResources.List()); err != nil {
				klog.Errorf("error installing syncer: %v", err)
				conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorInstallingSyncerReason, conditionsv1alpha1.ConditionSeverityError, "Error installing syncer: %v", err.Error())
				return nil // Don't retry.
			}

			klog.Info("syncer installing...")
			conditions.MarkTrue(cluster, clusterv1alpha1.ClusterReadyCondition)
		case SyncerModeNone:
			klog.Info("started none mode syncer!")
			conditions.MarkTrue(cluster, clusterv1alpha1.ClusterReadyCondition)
		}
		cluster.Status.SyncedResources = groupResources.List()
	}

	if conditions.IsTrue(cluster, clusterv1alpha1.ClusterReadyCondition) {
		if c.syncerMode == SyncerModePull {
			if err := healthcheckSyncer(ctx, client, logicalCluster); err != nil {
				klog.Error("syncer not yet ready")
				conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ClusterNotReadyReason, conditionsv1alpha1.ConditionSeverityInfo, "Syncer not yet ready")
			} else {
				klog.Infof("started pull mode syncer for cluster %s in logical cluster %s!", cluster.Name, logicalCluster)
				conditions.MarkTrue(cluster, clusterv1alpha1.ClusterReadyCondition)
			}
		} else {
			klog.Infof("healthy push mode syncer running for cluster %s in logical cluster %s!", cluster.Name, logicalCluster)
			conditions.MarkTrue(cluster, clusterv1alpha1.ClusterReadyCondition)
		}
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

func (c *Controller) cleanup(ctx context.Context, deletedCluster *clusterv1alpha1.Cluster) {
	klog.Infof("cleanup resources for cluster %q", deletedCluster.Name)

	if apiImporter := c.apiImporters[deletedCluster.Name]; apiImporter != nil {
		apiImporter.Stop()
		delete(c.apiImporters, deletedCluster.Name)
	}

	switch c.syncerMode {
	case SyncerModePull:
		// Get client from kubeconfig
		cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(deletedCluster.Spec.KubeConfig))
		if err != nil {
			klog.Errorf("invalid kubeconfig: %v", err)
			return
		}
		client, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			klog.Errorf("error creating client: %v", err)
			return
		}

		uninstallSyncer(ctx, client)
	case SyncerModePush:
		syncerCancel, ok := c.syncerCancelFuncs[deletedCluster.Name]
		if !ok {
			klog.Errorf("could not find syncer for cluster %q", deletedCluster.Name)
			return
		}
		klog.Infof("stopping syncer for cluster %q", deletedCluster.Name)
		syncerCancel()
		delete(c.syncerCancelFuncs, deletedCluster.Name)
	}
}
