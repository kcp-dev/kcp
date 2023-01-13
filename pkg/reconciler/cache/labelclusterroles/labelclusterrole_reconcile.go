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

package labelclusterroles

import (
	"context"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"

	kcpcorehelper "github.com/kcp-dev/kcp/pkg/apis/core/helper"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

func (c *controller) reconcile(ctx context.Context, rb *rbacv1.ClusterRole) (bool, error) {
	r := &reconciler{
		groupName:                    c.groupName,
		isRelevantClusterRole:        c.isRelevantClusterRole,
		isRelevantClusterRoleBinding: c.isRelevantClusterRoleBinding,
		getReferencingClusterRoleBindings: func(cluster logicalcluster.Name, name string) ([]*rbacv1.ClusterRoleBinding, error) {
			key := kcpcache.ToClusterAwareKey(cluster.String(), "", name)
			return indexers.ByIndex[*rbacv1.ClusterRoleBinding](c.clusterRoleBindingIndexer, ClusterRoleBindingByClusterRoleName, key)
		},
	}
	return r.reconcile(ctx, rb)
}

type reconciler struct {
	groupName string

	isRelevantClusterRole        func(cr *rbacv1.ClusterRole) bool
	isRelevantClusterRoleBinding func(crb *rbacv1.ClusterRoleBinding) bool

	getReferencingClusterRoleBindings func(cluster logicalcluster.Name, name string) ([]*rbacv1.ClusterRoleBinding, error)
}

func (r *reconciler) reconcile(ctx context.Context, cr *rbacv1.ClusterRole) (bool, error) {
	logger := klog.FromContext(ctx)

	replicate := r.isRelevantClusterRole(cr)
	if !replicate {
		objs, err := r.getReferencingClusterRoleBindings(logicalcluster.From(cr), cr.Name)
		if err != nil {
			runtime.HandleError(err)
			return false, nil // nothing we can do
		}
		for _, crb := range objs {
			if r.isRelevantClusterRoleBinding(crb) {
				replicate = true
				break
			}
		}
	}

	if replicate {
		var changed bool
		if cr.Annotations, changed = kcpcorehelper.ReplicateFor(cr.Annotations, r.groupName); changed {
			logger.V(2).Info("Replicating ClusterRole")
		}
	} else {
		var changed bool
		if cr.Annotations, changed = kcpcorehelper.DontReplicateFor(cr.Annotations, r.groupName); changed {
			logger.V(2).Info("Not replicating ClusterRole")
		}
	}

	return false, nil
}
