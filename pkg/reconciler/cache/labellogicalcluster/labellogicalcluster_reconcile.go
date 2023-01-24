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

package labellogicalcluster

import (
	"context"

	"k8s.io/klog/v2"

	kcpcorehelper "github.com/kcp-dev/kcp/pkg/apis/core/helper"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
)

func (c *controller) reconcile(ctx context.Context, cluster *corev1alpha1.LogicalCluster) (bool, error) {
	r := &reconciler{
		groupName:                c.groupName,
		isRelevantLogicalCluster: c.isRelevantLogicalCluster,
	}
	return r.reconcile(ctx, cluster)
}

type reconciler struct {
	groupName string

	isRelevantLogicalCluster func(cluster *corev1alpha1.LogicalCluster) bool
}

func (r *reconciler) reconcile(ctx context.Context, cluster *corev1alpha1.LogicalCluster) (bool, error) {
	logger := klog.FromContext(ctx)

	if replicate := r.isRelevantLogicalCluster(cluster); replicate {
		var changed bool
		if cluster.Annotations, changed = kcpcorehelper.ReplicateFor(cluster.Annotations, r.groupName); changed {
			logger.V(2).Info("Replicating LogicalCluster")
		}
	} else {
		var changed bool
		if cluster.Annotations, changed = kcpcorehelper.DontReplicateFor(cluster.Annotations, r.groupName); changed {
			logger.V(2).Info("Not replicating LogicalCluster")
		}
	}

	return false, nil
}
