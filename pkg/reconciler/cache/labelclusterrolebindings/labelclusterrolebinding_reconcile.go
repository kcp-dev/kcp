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
	controlplaneapiserver "k8s.io/kubernetes/pkg/controlplane/apiserver"

	"github.com/kcp-dev/logicalcluster/v3"

	kcpcorehelper "github.com/kcp-dev/kcp/sdk/apis/core/helper"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
)

func (c *controller) reconcile(ctx context.Context, crb *rbacv1.ClusterRoleBinding) (bool, error) {
	r := &reconciler{
		groupName:                    c.groupName,
		isRelevantClusterRole:        c.isRelevantClusterRole,
		isRelevantClusterRoleBinding: c.isRelevantClusterRoleBinding,
		getClusterRole: func(cluster logicalcluster.Name, name string) (*rbacv1.ClusterRole, error) {
			obj, err := c.clusterRoleLister.Cluster(cluster).Get(name)
			if err != nil && !errors.IsNotFound(err) {
				return nil, err
			} else if errors.IsNotFound(err) {
				return c.clusterRoleLister.Cluster(controlplaneapiserver.LocalAdminCluster).Get(name)
			}
			return obj, nil
		},
	}
	return r.reconcile(ctx, crb)
}

type reconciler struct {
	groupName                    string
	isRelevantClusterRole        func(clusterName logicalcluster.Name, cr *rbacv1.ClusterRole) bool
	isRelevantClusterRoleBinding func(clusterName logicalcluster.Name, crb *rbacv1.ClusterRoleBinding) bool
	getClusterRole               func(cluster logicalcluster.Name, name string) (*rbacv1.ClusterRole, error)
}

func (r *reconciler) reconcile(ctx context.Context, crb *rbacv1.ClusterRoleBinding) (bool, error) {
	logger := klog.FromContext(ctx)

	clusterName := logicalcluster.From(crb)

	// is a maximum-permission-policy subject?
	replicate := r.isRelevantClusterRoleBinding(clusterName, crb)

	// references relevant ClusterRole?
	if !replicate && crb.RoleRef.Kind == "ClusterRole" && crb.RoleRef.APIGroup == rbacv1.GroupName {
		localCR, err := r.getClusterRole(clusterName, crb.RoleRef.Name)
		if err != nil && !errors.IsNotFound(err) {
			return false, err
		}
		if localCR != nil && r.isRelevantClusterRole(clusterName, localCR) {
			replicate = true
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
