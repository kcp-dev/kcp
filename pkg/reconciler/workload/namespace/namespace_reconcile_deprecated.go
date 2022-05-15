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

package namespace

import (
	"context"

	"github.com/kcp-dev/logicalcluster"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const (
	DeprecatedScheduledClusterNamespaceLabel = "workloads.kcp.dev/cluster"
)

// ensureScheduled attempts to ensure the namespace is assigned to a viable cluster. This
// will succeed without error if a cluster is assigned or if there are no viable clusters
// to assign to. The condition of not being scheduled to a cluster will be reflected in
// the namespace's status rather than by returning an error.
func (c *Controller) ensureNamespaceScheduledDeprecated(ctx context.Context, ns *corev1.Namespace) (*corev1.Namespace, bool, error) {
	oldPClusterName := ns.Labels[DeprecatedScheduledClusterNamespaceLabel]

	scheduler := namespaceScheduler{
		getCluster:   c.clusterLister.Get,
		listClusters: c.clusterLister.List,
	}
	newPClusterName, err := scheduler.AssignCluster(ns)
	if err != nil {
		return ns, false, err
	}

	if oldPClusterName == newPClusterName {
		return ns, false, nil
	}

	klog.V(2).Infof("Patching to update cluster assignment for namespace %s|%s: %s -> %s",
		logicalcluster.From(ns), ns.Name, oldPClusterName, newPClusterName)
	patchType, patchBytes, err := schedulingClusterLabelPatchBytes(oldPClusterName, newPClusterName)
	if err != nil {
		klog.Errorf("Failed to create patch for cluster assignment: %v", err)
		return ns, false, err
	}

	patchedNamespace, err := c.kubeClient.Cluster(logicalcluster.From(ns)).CoreV1().Namespaces().
		Patch(ctx, ns.Name, patchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return ns, false, err
	}

	return patchedNamespace, true, nil
}
