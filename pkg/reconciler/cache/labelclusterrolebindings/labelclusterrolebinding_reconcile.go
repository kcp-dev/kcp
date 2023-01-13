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

package labelclusterrolebindings

import (
	"context"

	"github.com/kcp-dev/logicalcluster/v3"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"

	kcpcorehelper "github.com/kcp-dev/kcp/pkg/apis/core/helper"
)

func (c *controller) reconcile(ctx context.Context, crb *rbacv1.ClusterRoleBinding) (bool, error) {
	r := &reconciler{
		groupName:                    c.groupName,
		isRelevantClusterRole:        c.isRelevantClusterRole,
		isRelevantClusterRoleBinding: c.isRelevantClusterRoleBinding,
		getClusterRole: func(cluster logicalcluster.Name, name string) (*rbacv1.ClusterRole, error) {
			return c.clusterRoleLister.Cluster(cluster).Get(name)
		},
	}
	return r.reconcile(ctx, crb)
}

type reconciler struct {
	groupName                    string
	isRelevantClusterRole        func(cr *rbacv1.ClusterRole) bool
	isRelevantClusterRoleBinding func(crb *rbacv1.ClusterRoleBinding) bool
	getClusterRole               func(cluster logicalcluster.Name, name string) (*rbacv1.ClusterRole, error)
}

func (r *reconciler) reconcile(ctx context.Context, crb *rbacv1.ClusterRoleBinding) (bool, error) {
	logger := klog.FromContext(ctx)

	// is a maximum-permission-policy subject?
	replicate := r.isRelevantClusterRoleBinding(crb)

	// references relevant ClusterRole?
	if !replicate && crb.RoleRef.Kind == "ClusterRole" && crb.RoleRef.APIGroup == rbacv1.GroupName {
		localCR, err := r.getClusterRole(logicalcluster.From(crb), crb.RoleRef.Name)
		if err != nil && !errors.IsNotFound(err) {
			return false, err
		}
		if localCR != nil && r.isRelevantClusterRole(localCR) {
			replicate = true
		} else {
			// fall back to possible bootstrap ClusterRole
			bootstrapCR, err := r.getClusterRole("system:admin", crb.RoleRef.Name)
			if err != nil && !errors.IsNotFound(err) {
				return false, err
			}
			if bootstrapCR != nil && r.isRelevantClusterRole(bootstrapCR) {
				replicate = true
			}
		}
	}

	// calculate patch
	if replicate {
		var changed bool
		if crb.Annotations, changed = kcpcorehelper.ReplicateFor(crb.Annotations, r.groupName); changed {
			logger.V(2).Info("Replicating ClusterRoleBinding")
		}
	} else {
		var changed bool
		if crb.Annotations, changed = kcpcorehelper.DontReplicateFor(crb.Annotations, r.groupName); changed {
			logger.V(2).Info("Not replicating ClusterRoleBinding")
		}
	}

	return false, nil
}
