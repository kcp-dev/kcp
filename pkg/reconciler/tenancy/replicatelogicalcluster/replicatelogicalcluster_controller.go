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

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	corev1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/core/v1alpha1"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/labellogicalcluster"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
)

const (
	ControllerName = "kcp-tenancy-replicate-logicalcluster"
)

// NewController returns a new controller for labelling LogicalClusters that should be replicated.

func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	workspaceTypeInformer tenancyv1alpha1informers.WorkspaceTypeClusterInformer,
) labellogicalcluster.Controller {
	logicalClusterLister := logicalClusterInformer.Lister()
	workspaceTypeIndexer := workspaceTypeInformer.Informer().GetIndexer()

	c := labellogicalcluster.NewController(
		ControllerName,
		tenancy.GroupName,
		func(cluster *corev1alpha1.LogicalCluster) bool {
			// If there are any WorkspaceTypes for this logical cluster, then the LogicalCluster object should be replicated.
			keys, err := workspaceTypeIndexer.IndexKeys(kcpcache.ClusterIndexName, kcpcache.ClusterIndexKey(logicalcluster.From(cluster)))
			if err != nil {
				runtime.HandleError(fmt.Errorf("failed to list WorkspaceTypes: %v", err))
				return false
			}
			return len(keys) > 0
		},
		kcpClusterClient,
		logicalClusterInformer,
	)

	// enqueue the logical cluster every time a Workspace changes
	enqueueWorkspaceType := func(obj interface{}) {
		if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			obj = tombstone.Obj
		}

		workspaceType, ok := obj.(*tenancyv1alpha1.WorkspaceType)
		if !ok {
			runtime.HandleError(fmt.Errorf("unexpected object type: %T", obj))
			return
		}

		cluster, err := logicalClusterLister.Cluster(logicalcluster.From(workspaceType)).Get(corev1alpha1.LogicalClusterName)
		if err != nil && !apierrors.IsNotFound(err) {
			runtime.HandleError(fmt.Errorf("failed to get logical cluster: %v", err))
			return
		} else if !apierrors.IsNotFound(err) {
			return
		}

		c.EnqueueLogicalCluster(cluster, "reason", "WorkspaceType changed", "workspacetype", workspaceType.Name)
	}

	workspaceTypeInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: replication.IsNoSystemClusterName,
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				enqueueWorkspaceType(obj)
			},
			DeleteFunc: func(obj interface{}) {
				enqueueWorkspaceType(obj)
			},
		},
	})

	return c
}
