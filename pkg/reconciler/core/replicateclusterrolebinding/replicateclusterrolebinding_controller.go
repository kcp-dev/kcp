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

package replicateclusterrolebinding

import (
	kcprbacinformers "github.com/kcp-dev/client-go/informers/rbac/v1"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/kcp/pkg/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/core/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/labelclusterrolebindings"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
	"github.com/kcp-dev/kcp/pkg/reconciler/core/replicateclusterrole"
)

const (
	ControllerName = "kcp-core-replicate-clusterrolebinding"
)

// NewController returns a new controller for labelling ClusterRoleBindings that should be replicated.
func NewController(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	clusterRoleBindingInformer kcprbacinformers.ClusterRoleBindingClusterInformer,
	clusterRoleInformer kcprbacinformers.ClusterRoleClusterInformer,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
) labelclusterrolebindings.Controller {
	c := labelclusterrolebindings.NewController(
		ControllerName,
		core.GroupName,
		func(clusterName logicalcluster.Name, cr *rbacv1.ClusterRole) bool {
			// only replicate if LogicalCluster is replicated
			cluster, err := logicalClusterInformer.Lister().Cluster(clusterName).Get(corev1alpha1.LogicalClusterName) // cannot use the cluster name from cr because it might be system:admin
			if err != nil {
				return false
			}
			return cluster.Annotations[core.ReplicateAnnotationKey] != "" && replicateclusterrole.HasAccessRule(cr)
		},
		func(clusterName logicalcluster.Name, crb *rbacv1.ClusterRoleBinding) bool { return false },
		kubeClusterClient,
		clusterRoleBindingInformer,
		clusterRoleInformer,
	)

	// requeue all ClusterRoleBindings when a LogicalCluster changes replication status
	logicalClusterInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: replication.IsNoSystemClusterName,
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				cluster := obj.(*corev1alpha1.LogicalCluster)
				c.EnqueueClusterRoleBindings("reason", "LogicalCluster added", "logicalcluster", logicalcluster.From(cluster).String())
			},
			UpdateFunc: func(old, obj interface{}) {
				oldCluster, ok := old.(*corev1alpha1.LogicalCluster)
				if !ok {
					return
				}
				newCluster, ok := obj.(*corev1alpha1.LogicalCluster)
				if !ok {
					return
				}
				if (oldCluster.Annotations[core.ReplicateAnnotationKey] == "") != (newCluster.Annotations[core.ReplicateAnnotationKey] == "") {
					c.EnqueueClusterRoleBindings("reason", "LogicalCluster changed replication status", "logicalcluster", logicalcluster.From(newCluster).String())
				}
			},
		},
	})

	return c
}
