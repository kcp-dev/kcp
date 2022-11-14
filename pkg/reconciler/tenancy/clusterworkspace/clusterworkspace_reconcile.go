/*
Copyright 2021 The KCP Authors.

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

package clusterworkspace

import (
	"context"

	"github.com/kcp-dev/logicalcluster/v2"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
)

type reconcileStatus int

const (
	reconcileStatusStopAndRequeue reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error)
}

func (c *Controller) reconcile(ctx context.Context, ws *tenancyv1alpha1.ClusterWorkspace) (bool, error) {
	reconcilers := []reconciler{
		&metaDataReconciler{},
		&preThisWorkspaceReconciler{},
		&schedulingReconciler{
			getShard: func(name string) (*tenancyv1alpha1.ClusterWorkspaceShard, error) {
				return c.clusterWorkspaceShardLister.Cluster(tenancyv1alpha1.RootCluster).Get(name)
			},
			getShardByHash: func(hash string) (*tenancyv1alpha1.ClusterWorkspaceShard, error) {
				shards, err := c.clusterWorkspaceShardIndexer.ByIndex(byBase36Sha224Name, hash)
				if err != nil {
					return nil, err
				}
				if len(shards) == 0 {
					return nil, nil
				}
				return shards[0].(*tenancyv1alpha1.ClusterWorkspaceShard), nil
			},
			listShards:                c.clusterWorkspaceShardLister.List,
			logicalClusterAdminConfig: c.logicalClusterAdminConfig,
		},
		&phaseReconciler{
			getShardWithQuorum: func(ctx context.Context, name string, options metav1.GetOptions) (*tenancyv1alpha1.ClusterWorkspaceShard, error) {
				return c.kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1().ClusterWorkspaceShards().Get(ctx, name, options)
			},
			getAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
				return c.apiBindingLister.Cluster(clusterName).List(labels.Everything())
			},
		},
		&thisWorkspaceReconciler{
			getThisWorkspace: func(clusterName logicalcluster.Name) (*tenancyv1alpha1.ThisWorkspace, error) {
				return c.thisWorkspaceLister.Cluster(clusterName).Get(tenancyv1alpha1.ThisWorkspaceName)
			},
			createThisWorkspace: func(ctx context.Context, clusterName logicalcluster.Name, this *tenancyv1alpha1.ThisWorkspace) (*tenancyv1alpha1.ThisWorkspace, error) {
				return c.kcpClusterClient.TenancyV1alpha1().ThisWorkspaces().Cluster(clusterName).Create(ctx, this, metav1.CreateOptions{})
			},
			deleteThisWorkspace: func(ctx context.Context, clusterName logicalcluster.Name) error {
				return c.kcpClusterClient.TenancyV1alpha1().ThisWorkspaces().Cluster(clusterName).Delete(ctx, tenancyv1alpha1.ThisWorkspaceName, metav1.DeleteOptions{})
			},
			updateThisWorkspace: func(ctx context.Context, clusterName logicalcluster.Name, this *tenancyv1alpha1.ThisWorkspace) (*tenancyv1alpha1.ThisWorkspace, error) {
				return c.kcpClusterClient.TenancyV1alpha1().ThisWorkspaces().Update(logicalcluster.WithCluster(ctx, clusterName), this, metav1.UpdateOptions{})
			},
			updateThisWorkspaceStatus: func(ctx context.Context, clusterName logicalcluster.Name, this *tenancyv1alpha1.ThisWorkspace) (*tenancyv1alpha1.ThisWorkspace, error) {
				return c.kcpClusterClient.TenancyV1alpha1().ThisWorkspaces().Cluster(clusterName).UpdateStatus(ctx, this, metav1.UpdateOptions{})
			},
			getClusterRoleBinding: func(clusterName logicalcluster.Name, name string) (*rbacv1.ClusterRoleBinding, error) {
				return c.clusterRoleBindingLister.Cluster(clusterName).Get(name)
			},
			createClusterRoleBinding: func(ctx context.Context, clusterName logicalcluster.Name, binding *rbacv1.ClusterRoleBinding) (*rbacv1.ClusterRoleBinding, error) {
				return c.kubeClusterClient.RbacV1().ClusterRoleBindings().Cluster(clusterName).Create(ctx, binding, metav1.CreateOptions{})
			},
			updateClusterRoleBinding: func(ctx context.Context, clusterName logicalcluster.Name, binding *rbacv1.ClusterRoleBinding) (*rbacv1.ClusterRoleBinding, error) {
				return c.kubeClusterClient.RbacV1().ClusterRoleBindings().Cluster(clusterName).Update(ctx, binding, metav1.UpdateOptions{})
			},
			listClusterRoleBindings: func(clusterName logicalcluster.Name) ([]*rbacv1.ClusterRoleBinding, error) {
				return c.clusterRoleBindingLister.Cluster(clusterName).List(labels.Everything())
			},
			deleteClusterRoleBinding: func(ctx context.Context, clusterName logicalcluster.Name, name string) error {
				return c.kubeClusterClient.RbacV1().ClusterRoleBindings().Cluster(clusterName).Delete(ctx, name, metav1.DeleteOptions{})
			},
			getClusterRole: func(clusterName logicalcluster.Name, name string) (*rbacv1.ClusterRole, error) {
				return c.clusterRoleLister.Cluster(clusterName).Get(name)
			},
		},
		&workspaceReconciler{
			getWorkspace: func(clusterName logicalcluster.Name, name string) (*tenancyv1beta1.Workspace, error) {
				return c.workspaceLister.Cluster(clusterName).Get(name)
			},
			deleteWorkspaceWithoutProjection: func(ctx context.Context, clusterName logicalcluster.Name, name string) error {
				return c.kcpClusterClient.TenancyV1beta1().Workspaces().Cluster(clusterName).Delete(ctx, name, metav1.DeleteOptions{})
			},
			createWorkspaceWithoutProjection: func(ctx context.Context, clusterName logicalcluster.Name, workspace *tenancyv1beta1.Workspace) (*tenancyv1beta1.Workspace, error) {
				return c.kcpClusterClient.TenancyV1beta1().Workspaces().Cluster(clusterName).Create(ctx, workspace, metav1.CreateOptions{})
			},
			updateWorkspaceWithoutProjection: func(ctx context.Context, clusterName logicalcluster.Name, workspace *tenancyv1beta1.Workspace) (*tenancyv1beta1.Workspace, error) {
				return c.kcpClusterClient.TenancyV1beta1().Workspaces().Cluster(clusterName).Update(ctx, workspace, metav1.UpdateOptions{})
			},
			updateWorkspaceStatusWithoutProjection: func(ctx context.Context, clusterName logicalcluster.Name, this *tenancyv1beta1.Workspace) (*tenancyv1beta1.Workspace, error) {
				return c.kcpClusterClient.TenancyV1beta1().Workspaces().Cluster(clusterName).UpdateStatus(ctx, this, metav1.UpdateOptions{})
			},
		},
	}

	var errs []error

	requeue := false
	for _, r := range reconcilers {
		var err error
		var status reconcileStatus
		status, err = r.reconcile(ctx, ws)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStopAndRequeue {
			requeue = true
			break
		}
	}

	return requeue, utilserrors.NewAggregate(errs)
}
