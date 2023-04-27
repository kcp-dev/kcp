/*
Copyright 2023 The KCP Authors.

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

package replicatelogicalcluster

import (
	"fmt"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/kcp/pkg/reconciler/cache/labellogicalcluster"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/workload"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	workloadv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/workload/v1alpha1"
)

const (
	ControllerName = "kcp-workload-replicate-logicalcluster"
)

// NewController returns a new controller for labelling LogicalClusters that should be replicated.

func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	syncTargetInformer workloadv1alpha1informers.SyncTargetClusterInformer,
) labellogicalcluster.Controller {
	logicalClusterLister := logicalClusterInformer.Lister()
	syncTargetIndexer := syncTargetInformer.Informer().GetIndexer()

	c := labellogicalcluster.NewController(
		ControllerName,
		workload.GroupName,
		func(cluster *corev1alpha1.LogicalCluster) bool {
			// If there are any SyncTargets for this logical cluster, then the LogicalCluster object should be replicated.
			keys, err := syncTargetIndexer.IndexKeys(kcpcache.ClusterIndexName, kcpcache.ClusterIndexKey(logicalcluster.From(cluster)))
			if err != nil {
				runtime.HandleError(fmt.Errorf("failed to list SyncTargets: %v", err))
				return false
			}
			return len(keys) > 0
		},
		kcpClusterClient,
		logicalClusterInformer,
	)

	// enqueue the logical cluster every time the APIExport changes
	enqueueSyncTarget := func(obj interface{}) {
		if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			obj = tombstone.Obj
		}

		syncTarget, ok := obj.(*workloadv1alpha1.SyncTarget)
		if !ok {
			runtime.HandleError(fmt.Errorf("unexpected object type: %T", obj))
			return
		}

		cluster, err := logicalClusterLister.Cluster(logicalcluster.From(syncTarget)).Get(corev1alpha1.LogicalClusterName)
		if err != nil && !apierrors.IsNotFound(err) {
			runtime.HandleError(fmt.Errorf("failed to get logical cluster: %v", err))
			return
		} else if apierrors.IsNotFound(err) {
			return
		}

		c.EnqueueLogicalCluster(cluster, "reason", "SyncTarget changed", "synctarget", syncTarget.Name)
	}

	_, _ = syncTargetInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: replication.IsNoSystemClusterName,
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				enqueueSyncTarget(obj)
			},
			DeleteFunc: func(obj interface{}) {
				enqueueSyncTarget(obj)
			},
		},
	})

	return c
}
