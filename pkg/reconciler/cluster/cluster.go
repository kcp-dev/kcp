package cluster

import (
	"context"
	"fmt"
	"time"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/syncer"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"
)

const (
	pollInterval     = time.Minute
	numSyncerThreads = 2
)

func clusterOriginLabel(clusterID string) string {
	return "imported-from/" + clusterID
}

func (c *Controller) reconcile(ctx context.Context, cluster *clusterv1alpha1.Cluster) error {
	klog.Infof("reconciling cluster %q", cluster.Name)

	logicalCluster := cluster.GetClusterName()

	// Get client from kubeconfig
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(cluster.Spec.KubeConfig))
	if err != nil {
		klog.Errorf("invalid kubeconfig: %v", err)
		cluster.Status.SetConditionReady(corev1.ConditionFalse,
			"InvalidKubeConfig",
			fmt.Sprintf("Invalid kubeconfig: %v", err))
		return nil // Don't retry.
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		klog.Errorf("error creating client: %v", err)
		cluster.Status.SetConditionReady(corev1.ConditionFalse,
			"ErrorCreatingClient",
			fmt.Sprintf("Error creating client from kubeconfig: %v", err))
		return nil // Don't retry.
	}

	if c.apiImporters[cluster.Name] == nil {
		apiImporter, err := c.StartAPIImporter(cfg, cluster.Name, logicalCluster, time.Minute)
		if err != nil {
			klog.Errorf("error starting the API importer: %v", err)
			cluster.Status.SetConditionReady(corev1.ConditionFalse,
				"ErrorStartingAPIImporter",
				fmt.Sprintf("Error starting the API Importer: %v", err))
			return nil // Don't retry.
		}
		c.apiImporters[cluster.Name] = apiImporter
	}

	objs, err := c.apiresourceImportIndexer.ByIndex(LocationInLogicalClusterIndexName, GetLocationInLogicalClusterIndexKey(cluster.Name, logicalCluster))
	if err != nil {
		klog.Errorf("error in cluster reconcile: %v", err)
		return err
	}

	apiGroups := sets.NewString()
	groupResources := sets.NewString()

	for _, obj := range objs {
		apiResourceImport := obj.(*apiresourcev1alpha1.APIResourceImport)
		if apiResourceImport.IsConditionTrue(apiresourcev1alpha1.Compatible) && apiResourceImport.IsConditionTrue(apiresourcev1alpha1.Available) {
			apiGroups.Insert(apiResourceImport.Spec.GroupVersion.APIGroup())
			groupResources.Insert(schema.GroupResource{
				Group:    apiResourceImport.Spec.GroupVersion.APIGroup(),
				Resource: apiResourceImport.Spec.Plural,
			}.String())
		}
	}

	if !sets.NewString(cluster.Status.SyncedResources...).Equal(groupResources) {
		kubeConfig := c.kubeconfig.DeepCopy()
		if _, exists := kubeConfig.Contexts[logicalCluster]; !exists {
			klog.Errorf("error installing syncer: no context with the name of the expected cluster: %s", logicalCluster)
			cluster.Status.SetConditionReady(corev1.ConditionFalse,
				"ErrorInstallingSyncer",
				fmt.Sprintf("Error installing syncer: no context with the name of the expected cluster: %s", logicalCluster))
			return nil // Don't retry.
		}

		switch c.syncerMode {
		case SyncerModePush:
			if syncer := c.syncers[cluster.Name]; syncer != nil {
				syncer.Stop()
			}
			kubeConfig.CurrentContext = logicalCluster
			upstream, err := clientcmd.NewNonInteractiveClientConfig(*kubeConfig, logicalCluster, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
			if err != nil {
				klog.Errorf("error getting kcp kubeconfig: %v", err)
				cluster.Status.SetConditionReady(corev1.ConditionFalse,
					"ErrorStartingSyncer",
					fmt.Sprintf("Error starting syncer: %v", err))
				return nil // Don't retry.
			}

			downstream, err := clientcmd.RESTConfigFromKubeConfig([]byte(cluster.Spec.KubeConfig))
			if err != nil {
				klog.Errorf("error getting cluster kubeconfig: %v", err)
				cluster.Status.SetConditionReady(corev1.ConditionFalse,
					"ErrorStartingSyncer",
					fmt.Sprintf("Error starting syncer: %v", err))
				return nil // Don't retry.
			}

			syncer, err := syncer.StartSyncer(upstream, downstream, groupResources, cluster.Name, numSyncerThreads)
			if err != nil {
				klog.Errorf("error starting syncer in push mode: %v", err)
				cluster.Status.SetConditionReady(corev1.ConditionFalse,
					"ErrorStartingSyncer",
					fmt.Sprintf("Error starting syncer: %v", err))
				return err
			}
			c.syncers[cluster.Name] = syncer

			klog.Info("syncer ready!")
			cluster.Status.SetConditionReady(corev1.ConditionTrue,
				"SyncerReady",
				"Syncer ready")
		case SyncerModePull:
			kubeConfig.CurrentContext = logicalCluster
			bytes, err := clientcmd.Write(*kubeConfig)
			if err != nil {
				klog.Errorf("error writing kubeconfig for syncer: %v", err)
				cluster.Status.SetConditionReady(corev1.ConditionFalse,
					"ErrorInstallingSyncer",
					fmt.Sprintf("Error installing syncer: %v", err))
				return nil // Don't retry.
			}
			if err := installSyncer(ctx, client, c.syncerImage, string(bytes), cluster.Name, logicalCluster, groupResources.List()); err != nil {
				klog.Errorf("error installing syncer: %v", err)
				cluster.Status.SetConditionReady(corev1.ConditionFalse,
					"ErrorInstallingSyncer",
					fmt.Sprintf("Error installing syncer: %v", err))
				return nil // Don't retry.
			}

			klog.Info("syncer installing...")
			cluster.Status.SetConditionReady(corev1.ConditionUnknown,
				"SyncerInstalling",
				"Installing syncer on cluster")
		case SyncerModeNone:
			klog.Info("syncer ready!")
			cluster.Status.SetConditionReady(corev1.ConditionTrue,
				"SyncerReady",
				"Syncer ready")
		}
		cluster.Status.SyncedResources = groupResources.List()
	}

	if cluster.Status.Conditions.HasReady() {
		if c.syncerMode == SyncerModePull {
			if err := healthcheckSyncer(ctx, client, logicalCluster); err != nil {
				klog.Error("syncer not yet ready")
				cluster.Status.SetConditionReady(corev1.ConditionFalse,
					"SyncerNotReady",
					err.Error())
			} else {
				klog.Info("syncer ready!")
				cluster.Status.SetConditionReady(corev1.ConditionTrue,
					"SyncerReady",
					"Syncer ready")
			}
		} else {
			klog.Info("syncer ready!")
			cluster.Status.SetConditionReady(corev1.ConditionTrue,
				"SyncerReady",
				"Syncer ready")
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
		s, ok := c.syncers[deletedCluster.Name]
		if !ok {
			klog.Errorf("could not find syncer for cluster %q", deletedCluster.Name)
			return
		}
		klog.Infof("stopping syncer for cluster %q", deletedCluster.Name)
		s.Stop()
		delete(c.syncers, deletedCluster.Name)
	}
}
