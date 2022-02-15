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

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

type pullSyncerManager struct {
	syncerImage string
}

func newPullSyncerManager(syncerImage string) syncerManagerImpl {
	return &pullSyncerManager{
		syncerImage: syncerImage,
	}
}

func (m *pullSyncerManager) name() string {
	return "kcp-pull-syncer-manager"
}

func (m *pullSyncerManager) needsUpdate(ctx context.Context, cluster *clusterv1alpha1.Cluster, client *kubernetes.Clientset, groupResources sets.String) (bool, error) {
	logicalCluster := cluster.GetClusterName()
	upToDate, err := isSyncerInstalledAndUpToDate(ctx, client, logicalCluster, m.syncerImage)
	if err != nil {
		klog.Errorf("error checking if syncer needs to be installed: %v", err)
		return false, err
	}
	return !upToDate && groupResources.Len() > 0, nil
}

func (m *pullSyncerManager) update(ctx context.Context, cluster *clusterv1alpha1.Cluster, client *kubernetes.Clientset, groupResources sets.String, kubeConfig *clientcmdapi.Config) (bool, error) {
	kubeConfig.CurrentContext = "root"
	bytes, err := clientcmd.Write(*kubeConfig)
	if err != nil {
		klog.Errorf("error writing kubeconfig for syncer: %v", err)
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorInstallingSyncerReason, conditionsv1alpha1.ConditionSeverityError, "Error writing kubeconfig for syncer: %v", err.Error())
		return false, nil // Don't retry.
	}
	logicalCluster := cluster.GetClusterName()
	if err := installSyncer(ctx, client, m.syncerImage, string(bytes), cluster.Name, logicalCluster, groupResources.List()); err != nil {
		klog.Errorf("error installing syncer: %v", err)
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorInstallingSyncerReason, conditionsv1alpha1.ConditionSeverityError, "Error installing syncer: %v", err.Error())
		return false, nil // Don't retry.
	}

	klog.Info("syncer installing...")
	conditions.MarkTrue(cluster, clusterv1alpha1.ClusterReadyCondition)

	return true, nil
}

func (m *pullSyncerManager) checkHealth(ctx context.Context, cluster *clusterv1alpha1.Cluster, client *kubernetes.Clientset) bool {
	logicalCluster := cluster.GetClusterName()
	if err := healthcheckSyncer(ctx, client, logicalCluster); err != nil {
		klog.Error("syncer not yet ready")
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ClusterNotReadyReason, conditionsv1alpha1.ConditionSeverityInfo, "Syncer not yet ready")
	} else {
		klog.Infof("started pull mode syncer for cluster %s in logical cluster %s!", cluster.Name, logicalCluster)
		conditions.MarkTrue(cluster, clusterv1alpha1.ClusterReadyCondition)
	}
	return true
}

// TODO(marun) Consider using a finalizer to guarantee removal
func (m *pullSyncerManager) cleanup(ctx context.Context, deletedCluster *clusterv1alpha1.Cluster) {
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
}
