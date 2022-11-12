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

package thisworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster/v2"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/pkg/logging"
)

// thisWorkspaceReconciler is a temporary reconciler that creates the following
// inside the workspace itself:
// 1. a ThisWorkspace object
// 2. a ClusterRoleBinding for admins of the ClusterWorkspace
// 3. a ClusterRoleBinding for access of the ClusterWorkspace
type thisWorkspaceReconciler struct {
	getThisWorkspace          func(clusterName logicalcluster.Name) (*tenancyv1alpha1.ThisWorkspace, error)
	createThisWorkspace       func(ctx context.Context, clusterName logicalcluster.Name, this *tenancyv1alpha1.ThisWorkspace) (*tenancyv1alpha1.ThisWorkspace, error)
	deleteThisWorkspace       func(ctx context.Context, clusterName logicalcluster.Name) error
	updateThisWorkspaceStatus func(ctx context.Context, clusterName logicalcluster.Name, this *tenancyv1alpha1.ThisWorkspace) (*tenancyv1alpha1.ThisWorkspace, error)

	getClusterRoleBinding    func(clusterName logicalcluster.Name, name string) (*rbacv1.ClusterRoleBinding, error)
	createClusterRoleBinding func(ctx context.Context, clusterName logicalcluster.Name, binding *rbacv1.ClusterRoleBinding) (*rbacv1.ClusterRoleBinding, error)
	updateClusterRoleBinding func(ctx context.Context, clusterName logicalcluster.Name, binding *rbacv1.ClusterRoleBinding) (*rbacv1.ClusterRoleBinding, error)
	listClusterRoleBindings  func(clusterName logicalcluster.Name, opts metav1.ListOptions) ([]*rbacv1.ClusterRoleBinding, error)
	deleteClusterRoleBinding func(ctx context.Context, clusterName logicalcluster.Name, name string) error
	getClusterRole           func(clusterName logicalcluster.Name, name string) (*rbacv1.ClusterRole, error)
}

func (r *thisWorkspaceReconciler) reconcile(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error) {
	logger := logging.WithReconciler(klog.FromContext(ctx), "thisWorkspace")

	// this reconciler only brings old workspaces into the new world with ThisWorkspace. Hence,
	// only handle workspaces on the root shard.
	if workspace.Status.Location.Current != tenancyv1alpha1.RootShard {
		return reconcileStatusContinue, nil
	}

	// not about new workspaces
	if workspace.Status.Phase == tenancyv1alpha1.WorkspacePhaseScheduling {
		return reconcileStatusContinue, nil
	}

	// do not compete with deletion
	if !workspace.DeletionTimestamp.IsZero() {
		logger.Info("Deleting ThisWorkspace")
		if err := r.deleteThisWorkspace(ctx, logicalcluster.From(workspace).Join(workspace.Name)); err != nil && !errors.IsNotFound(err) {
			return reconcileStatusStopAndRequeue, err
		}
		return reconcileStatusContinue, nil
	}

	if this, err := r.getThisWorkspace(logicalcluster.From(workspace).Join(workspace.Name)); err != nil && !errors.IsNotFound(err) {
		return reconcileStatusStopAndRequeue, err
	} else if errors.IsNotFound(err) {
		logger.Info("Creating ThisWorkspace")
		this, err := r.createThisWorkspace(ctx, logicalcluster.From(workspace).Join(workspace.Name), &tenancyv1alpha1.ThisWorkspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: tenancyv1alpha1.ThisWorkspaceName,
			},
			Spec: tenancyv1alpha1.ThisWorkspaceSpec{
				Type: workspace.Spec.Type,
			},
		})
		if err != nil && !errors.IsAlreadyExists(err) {
			return reconcileStatusStopAndRequeue, err
		} else if err == nil {
			logger.Info("Created ThisWorkspace", logging.From(this)...)
		}
	} else if this.Status.Phase != workspace.Status.Phase ||
		this.Status.URL != workspace.Status.BaseURL ||
		!reflect.DeepEqual(this.Status.Conditions, workspace.Status.Conditions) ||
		!reflect.DeepEqual(this.Status.Initializers, workspace.Status.Initializers) {

		orig := this
		this = this.DeepCopy()
		this.Status.Phase = workspace.Status.Phase
		this.Status.URL = workspace.Status.BaseURL
		this.Status.Conditions = workspace.Status.Conditions // TODO: this more than we show in Workspace today through projection. But we will get rid of the projection anyway. So we are lazy here now.
		this.Status.Initializers = workspace.Status.Initializers

		if this, err := r.updateThisWorkspaceStatus(ctx, logicalcluster.From(workspace).Join(workspace.Name), this); err != nil && !errors.IsConflict(err) {
			return reconcileStatusStopAndRequeue, err
		} else if errors.IsConflict(err) {
			return reconcileStatusStopAndRequeue, nil
		} else {
			logger.WithValues("patch", diff(orig.Status, this.Status)).Info("Updated ThisWorkspace", logging.From(this)...)
		}
	}

	// find admins and accessors
	adminUsers, adminGroups := sets.NewString(), sets.NewString()
	accessorUsers, accessorGroups := sets.NewString(), sets.NewString()
	bindings, err := r.listClusterRoleBindings(logicalcluster.From(workspace), metav1.ListOptions{})
	if err != nil {
		return reconcileStatusStopAndRequeue, err
	}
	for _, binding := range bindings {
		if binding.RoleRef.APIGroup != rbacv1.GroupName || binding.RoleRef.Kind != "ClusterRole" {
			continue
		}

		role, err := r.getClusterRole(logicalcluster.From(workspace), binding.RoleRef.Name)
		if err != nil && !errors.IsNotFound(err) {
			return reconcileStatusStopAndRequeue, err
		} else if errors.IsNotFound(err) {
			continue
		}

		for _, rule := range role.Rules {
			if !sets.NewString(rule.Resources...).Has("workspaces/content") {
				continue
			}
			if !sets.NewString(rule.ResourceNames...).Has(workspace.Name) {
				continue
			}
			verbs := sets.NewString(rule.Verbs...)
			for _, subject := range binding.Subjects {
				if subject.APIGroup != rbacv1.GroupName {
					continue
				}
				if subject.Kind == "User" && verbs.Has("access") && !strings.HasPrefix(subject.Name, "system:serviceaccount:") {
					accessorUsers.Insert(subject.Name)
				}
				if subject.Kind == "User" && verbs.Has("admin") && !strings.HasPrefix(subject.Name, "system:serviceaccounts:") {
					adminUsers.Insert(subject.Name)
				}
				if subject.Kind == "Group" && verbs.Has("access") && !strings.HasPrefix(subject.Name, "system:serviceaccount:") {
					accessorGroups.Insert(subject.Name)
				}
				if subject.Kind == "Group" && verbs.Has("admin") && !strings.HasPrefix(subject.Name, "system:serviceaccounts:") {
					adminGroups.Insert(subject.Name)
				}
			}
		}
	}
	logger.Info("Found accessors", "users", accessorUsers.List(), "groups", accessorGroups.List())
	logger.Info("Found admins", "users", adminUsers.List(), "groups", adminGroups.List())

	// update admins and accessors
	for _, role := range []struct {
		bindingName string
		users       sets.String
		groups      sets.String
		roleName    string
	}{
		{
			bindingName: "workspace-admin-legacy",
			users:       adminUsers,
			groups:      adminGroups,
			roleName:    "cluster-admin",
		},
		{
			bindingName: "workspace-access-legacy",
			users:       accessorUsers,
			groups:      accessorGroups,
			roleName:    bootstrap.SystemKcpWorkspaceAccessGroup,
		},
	} {
		if len(role.users) == 0 && len(role.groups) == 0 {
			if err := r.deleteClusterRoleBinding(ctx, logicalcluster.From(workspace).Join(workspace.Name), role.bindingName); err != nil && !errors.IsNotFound(err) {
				return reconcileStatusStopAndRequeue, err
			}
		} else {
			newBinding := &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: role.bindingName,
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: rbacv1.GroupName,
					Kind:     "ClusterRole",
					Name:     role.roleName,
				},
			}
			for _, user := range adminUsers.List() {
				newBinding.Subjects = append(newBinding.Subjects, rbacv1.Subject{
					APIGroup: rbacv1.GroupName,
					Kind:     "User",
					Name:     user,
				})
			}
			for _, group := range adminGroups.List() {
				newBinding.Subjects = append(newBinding.Subjects, rbacv1.Subject{
					APIGroup: rbacv1.GroupName,
					Kind:     "Group",
					Name:     group,
				})
			}

			if binding, err := r.getClusterRoleBinding(logicalcluster.From(workspace).Join(workspace.Name), newBinding.Name); err != nil && !errors.IsNotFound(err) {
				return reconcileStatusStopAndRequeue, err
			} else if errors.IsNotFound(err) {
				logger.Info("Creating ClusterRoleBinding")
				created, err := r.createClusterRoleBinding(ctx, logicalcluster.From(workspace).Join(workspace.Name), newBinding)
				if err != nil && !errors.IsAlreadyExists(err) {
					return reconcileStatusStopAndRequeue, err
				} else if err == nil {
					logger.Info("Created ClusterRoleBinding", logging.From(created)...)
				}
			} else {
				if !equality.Semantic.DeepEqual(binding.Subjects, newBinding.Subjects) {
					logger.Info("Updating ClusterRoleBinding")
					orig := binding
					binding = binding.DeepCopy()
					binding.Subjects = newBinding.Subjects
					updated, err := r.updateClusterRoleBinding(ctx, logicalcluster.From(workspace).Join(workspace.Name), binding)
					if err != nil {
						return reconcileStatusStopAndRequeue, err
					}
					logger.WithValues("patch", diff(orig, binding)).Info("Updated ClusterRoleBinding", logging.From(updated)...)
				}
			}
		}
	}

	return reconcileStatusContinue, nil
}

func diff(orig, new interface{}) string {
	origData, err := json.Marshal(orig)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	newData, err := json.Marshal(new)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	patchBytes, err := jsonpatch.CreateMergePatch(origData, newData)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	return string(patchBytes)
}
