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
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/client/clientset/versioned/scheme"
	"github.com/kcp-dev/kcp/pkg/syncer"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const numSyncerThreads = 2

type pushSyncerManager struct {
	syncerCancelFuncs map[string]func()
}

func newPushSyncerManager() syncerManagerImpl {
	return &pushSyncerManager{
		syncerCancelFuncs: map[string]func(){},
	}
}

func (m *pushSyncerManager) name() string {
	return "kcp-push-syncer-manager"
}

func (m *pushSyncerManager) needsUpdate(ctx context.Context, cluster *clusterv1alpha1.Cluster, client *kubernetes.Clientset, groupResources sets.String) (bool, error) {
	_, running := m.syncerCancelFuncs[cluster.Name]
	return !running || !sets.NewString(cluster.Status.SyncedResources...).Equal(groupResources), nil
}

func (m *pushSyncerManager) update(ctx context.Context, cluster *clusterv1alpha1.Cluster, client *kubernetes.Clientset, groupResources sets.String, kubeConfig *clientcmdapi.Config) (bool, error) {
	upstream, err := clientcmd.NewNonInteractiveClientConfig(*kubeConfig, "root", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		klog.Errorf("error getting kcp kubeconfig: %v", err)
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorStartingSyncerReason, conditionsv1alpha1.ConditionSeverityError, "Error getting kcp kubeconfig: %v", err.Error())
		return false, nil // Don't retry.
	}

	downstream, err := clientcmd.RESTConfigFromKubeConfig([]byte(cluster.Spec.KubeConfig))
	if err != nil {
		klog.Errorf("error getting cluster kubeconfig: %v", err)
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorStartingSyncerReason, conditionsv1alpha1.ConditionSeverityError, "Error getting cluster kubeconfig: %v", err.Error())
		return false, nil // Don't retry.
	}

	logicalCluster := cluster.GetClusterName()
	syncerCtx, syncerCancel := context.WithCancel(ctx)
	if err := syncer.StartSyncer(syncerCtx, upstream, downstream, groupResources, cluster.Name, logicalCluster, numSyncerThreads); err != nil {
		klog.Errorf("error starting syncer in push mode: %v", err)
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ErrorStartingSyncerReason, conditionsv1alpha1.ConditionSeverityError, "Error starting syncer in push mode: %v", err.Error())

		syncerCancel()

		return false, err
	}

	oldSyncerCancel := m.syncerCancelFuncs[cluster.Name]
	m.syncerCancelFuncs[cluster.Name] = syncerCancel
	if oldSyncerCancel != nil {
		oldSyncerCancel()
	}

	klog.Infof("started push mode syncer for cluster %s in logical cluster %s!", cluster.Name, logicalCluster)
	conditions.MarkTrue(cluster, clusterv1alpha1.ClusterReadyCondition)

	return true, nil
}

func (m *pushSyncerManager) checkHealth(ctx context.Context, cluster *clusterv1alpha1.Cluster, client *kubernetes.Clientset) bool {
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(cluster.Spec.KubeConfig))
	if err != nil {
		klog.Errorf("error getting cluster kubeconfig: %v", err)
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ClusterUnknownReason, conditionsv1alpha1.ConditionSeverityError, "Error getting cluster kubeconfig: %v", err.Error())
		return false // Don't retry.
	}

	cfg.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	restClient, err := rest.UnversionedRESTClientFor(cfg)
	if err != nil {
		klog.Errorf("error getting rest client: %v", err)
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ClusterUnknownReason, conditionsv1alpha1.ConditionSeverityError, "Error getting rest client: %v", err.Error())
		return false // Don't retry.
	}

	_, err = restClient.Get().AbsPath("/readyz").Timeout(5 * time.Second).DoRaw(ctx)
	if err != nil {
		conditions.MarkFalse(cluster, clusterv1alpha1.ClusterReadyCondition, clusterv1alpha1.ClusterNotReadyReason, conditionsv1alpha1.ConditionSeverityError, "Error getting /readyz: %v", err.Error())
		return false // Don't retry.
	}

	logicalCluster := cluster.GetClusterName()
	klog.Infof("healthy push mode syncer running for cluster %s in logical cluster %s!", cluster.Name, logicalCluster)
	conditions.MarkTrue(cluster, clusterv1alpha1.ClusterReadyCondition)

	return true
}

func (m *pushSyncerManager) cleanup(ctx context.Context, deletedCluster *clusterv1alpha1.Cluster) {
	syncerCancel, ok := m.syncerCancelFuncs[deletedCluster.Name]
	if !ok {
		klog.Errorf("could not find syncer for cluster %q", deletedCluster.Name)
		return
	}
	klog.Infof("stopping syncer for cluster %q", deletedCluster.Name)
	syncerCancel()
	delete(m.syncerCancelFuncs, deletedCluster.Name)
}
