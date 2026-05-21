/*
Copyright 2026 The kcp Authors.

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

package logicalcluster

import (
	"context"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/contextmanager"
	"github.com/kcp-dev/kcp/pkg/server/filters"
)

type inactiveReconciler struct {
	clusterContextManager *contextmanager.Manager[logicalcluster.Path]
}

func (r *inactiveReconciler) reconcile(ctx context.Context, logicalCluster *corev1alpha1.LogicalCluster) (reconcileStatus, error) {
	if r.clusterContextManager == nil {
		return reconcileStatusContinue, nil
	}
	if logicalCluster.Annotations[filters.InactiveAnnotation] == "true" {
		// Cancel connections for this cluster and wildcard connections.
		r.clusterContextManager.Cancel(logicalcluster.From(logicalCluster).Path())
		r.clusterContextManager.Cancel(logicalcluster.Wildcard)
	}
	return reconcileStatusContinue, nil
}
