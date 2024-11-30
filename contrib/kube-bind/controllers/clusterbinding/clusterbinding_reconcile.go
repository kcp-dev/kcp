/*
Copyright 2022 The Kube Bind Authors.

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

package clusterbinding

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/utils/pointer"

	kuberesources "github.com/kcp-dev/kcp/contrib/kube-bind/kubernetes/resources"
	kubebindv1alpha1 "github.com/kube-bind/kube-bind/pkg/apis/kubebind/v1alpha1"
	conditionsapi "github.com/kube-bind/kube-bind/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kube-bind/kube-bind/pkg/apis/third_party/conditions/util/conditions"
)

type reconciler struct {
	scope kubebindv1alpha1.Scope

	listServiceExports func(ns string) ([]*kubebindv1alpha1.APIServiceExport, error)

	getClusterRole    func(name string) (*rbacv1.ClusterRole, error)
	createClusterRole func(ctx context.Context, binding *rbacv1.ClusterRole) (*rbacv1.ClusterRole, error)
	updateClusterRole func(ctx context.Context, binding *rbacv1.ClusterRole) (*rbacv1.ClusterRole, error)

	getClusterRoleBinding    func(name string) (*rbacv1.ClusterRoleBinding, error)
	createClusterRoleBinding func(ctx context.Context, binding *rbacv1.ClusterRoleBinding) (*rbacv1.ClusterRoleBinding, error)
	updateClusterRoleBinding func(ctx context.Context, binding *rbacv1.ClusterRoleBinding) (*rbacv1.ClusterRoleBinding, error)
	deleteClusterRoleBinding func(ctx context.Context, name string) error

	getRoleBinding    func(ns, name string) (*rbacv1.RoleBinding, error)
	createRoleBinding func(ctx context.Context, ns string, binding *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error)
	updateRoleBinding func(ctx context.Context, ns string, binding *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error)

	getNamespace func(name string) (*corev1.Namespace, error)
}

func (r *reconciler) reconcile(ctx context.Context, clusterBinding *kubebindv1alpha1.ClusterBinding) error {
	var errs []error

	if err := r.ensureClusterBindingConditions(ctx, clusterBinding); err != nil {
		errs = append(errs, err)
	}
	if err := r.ensureRBACRoleBinding(ctx, clusterBinding); err != nil {
		errs = append(errs, err)
	}
	if err := r.ensureRBACClusterRole(ctx, clusterBinding); err != nil {
		errs = append(errs, err)
	}
	if err := r.ensureRBACClusterRoleBinding(ctx, clusterBinding); err != nil {
		errs = append(errs, err)
	}

	conditions.SetSummary(clusterBinding)

	return utilerrors.NewAggregate(errs)
}

func (r *reconciler) ensureClusterBindingConditions(ctx context.Context, clusterBinding *kubebindv1alpha1.ClusterBinding) error {
	if clusterBinding.Status.LastHeartbeatTime.IsZero() {
		conditions.MarkFalse(clusterBinding,
			kubebindv1alpha1.ClusterBindingConditionHealthy,
			"FirstHeartbeatPending",
			conditionsapi.ConditionSeverityInfo,
			"Waiting for first heartbeat",
		)
	} else if clusterBinding.Status.HeartbeatInterval.Duration == 0 {
		conditions.MarkFalse(clusterBinding,
			kubebindv1alpha1.ClusterBindingConditionHealthy,
			"HeartbeatIntervalMissing",
			conditionsapi.ConditionSeverityInfo,
			"Waiting for consumer cluster reporting its heartbeat interval",
		)
	} else if ago := time.Since(clusterBinding.Status.LastHeartbeatTime.Time); ago > clusterBinding.Status.HeartbeatInterval.Duration*2 {
		conditions.MarkFalse(clusterBinding,
			kubebindv1alpha1.ClusterBindingConditionHealthy,
			"HeartbeatTimeout",
			conditionsapi.ConditionSeverityError,
			"Heartbeat timeout: expected heartbeat within %s, but last one has been at %s",
			clusterBinding.Status.HeartbeatInterval.Duration,
			clusterBinding.Status.LastHeartbeatTime.Time, // do not put "ago" here. It will hotloop.
		)
	} else if ago < time.Second*10 {
		conditions.MarkFalse(clusterBinding,
			kubebindv1alpha1.ClusterBindingConditionHealthy,
			"HeartbeatTimeDrift",
			conditionsapi.ConditionSeverityWarning,
			"Clocks of consumer cluster and service account cluster seem to be off by more than 10s",
		)
	} else {
		conditions.MarkTrue(clusterBinding,
			kubebindv1alpha1.ClusterBindingConditionHealthy,
		)
	}

	return nil
}

func (r *reconciler) ensureRBACClusterRole(ctx context.Context, clusterBinding *kubebindv1alpha1.ClusterBinding) error {
	name := "kube-binder-" + clusterBinding.Namespace
	role, err := r.getClusterRole(name)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get ClusterRole %s: %w", name, err)
	}

	ns, err := r.getNamespace(clusterBinding.Namespace)
	if err != nil {
		return fmt.Errorf("failed to get Namespace %s: %w", clusterBinding.Namespace, err)
	}

	exports, err := r.listServiceExports(clusterBinding.Namespace)
	if err != nil {
		return fmt.Errorf("failed to list APIServiceExports: %w", err)
	}
	expected := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1",
					Kind:       "Namespace",
					Name:       clusterBinding.Namespace,
					Controller: pointer.Bool(true),
					UID:        ns.UID,
				},
			},
		},
	}
	for _, export := range exports {
		expected.Rules = append(expected.Rules, rbacv1.PolicyRule{
			APIGroups: []string{export.Spec.Group},
			Resources: []string{export.Spec.Names.Plural},
			Verbs:     []string{"get", "list", "watch", "update", "patch", "delete", "create"},
		})
	}

	if role == nil {
		if _, err := r.createClusterRole(ctx, expected); err != nil {
			return fmt.Errorf("failed to create ClusterRole %s: %w", expected.Name, err)
		}
	} else if !reflect.DeepEqual(role.Rules, expected.Rules) {
		role = role.DeepCopy()
		role.Rules = expected.Rules
		if _, err := r.updateClusterRole(ctx, role); err != nil {
			return fmt.Errorf("failed to create ClusterRole %s: %w", role.Name, err)
		}
	}

	return nil
}

func (r *reconciler) ensureRBACClusterRoleBinding(ctx context.Context, clusterBinding *kubebindv1alpha1.ClusterBinding) error {
	name := "kube-binder-" + clusterBinding.Namespace
	binding, err := r.getClusterRoleBinding(name)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get ClusterRoleBinding %s: %w", name, err)
	}

	if r.scope != kubebindv1alpha1.ClusterScope {
		if err := r.deleteClusterRoleBinding(ctx, name); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete ClusterRoleBinding %s: %w", name, err)
		}
	}

	ns, err := r.getNamespace(clusterBinding.Namespace)
	if err != nil {
		return fmt.Errorf("failed to get Namespace %s: %w", clusterBinding.Namespace, err)
	}

	expected := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "v1",
					Kind:       "Namespace",
					Name:       clusterBinding.Namespace,
					Controller: pointer.Bool(true),
					UID:        ns.UID,
				},
			},
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Namespace: clusterBinding.Namespace,
				Name:      kuberesources.ServiceAccountName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			Name:     name,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	if binding == nil {
		if _, err := r.createClusterRoleBinding(ctx, expected); err != nil {
			return fmt.Errorf("failed to create ClusterRoleBinding %s: %w", expected.Name, err)
		}
	} else if !reflect.DeepEqual(binding.Subjects, expected.Subjects) {
		binding = binding.DeepCopy()
		binding.Subjects = expected.Subjects
		// roleRef is immutable
		if _, err := r.updateClusterRoleBinding(ctx, binding); err != nil {
			return fmt.Errorf("failed to create ClusterRoleBinding %s: %w", expected.Namespace, err)
		}
	}

	return nil
}

func (r *reconciler) ensureRBACRoleBinding(ctx context.Context, clusterBinding *kubebindv1alpha1.ClusterBinding) error {
	binding, err := r.getRoleBinding(clusterBinding.Namespace, "kube-binder")
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get RoleBinding \"kube-binder\": %w", err)
	}

	expected := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: kuberesources.ServiceAccountName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      kuberesources.ServiceAccountName,
				Namespace: clusterBinding.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "kube-binder",
		},
	}

	if binding == nil {
		if _, err := r.createRoleBinding(ctx, clusterBinding.Namespace, expected); err != nil {
			return fmt.Errorf("failed to create RoleBinding %s: %w", expected.Name, err)
		}
	} else if !reflect.DeepEqual(binding.Subjects, expected.Subjects) {
		binding = binding.DeepCopy()
		binding.Subjects = expected.Subjects
		// roleRef is immutable
		if _, err := r.updateRoleBinding(ctx, clusterBinding.Namespace, binding); err != nil {
			return fmt.Errorf("failed to create RoleBinding %s: %w", expected.Namespace, err)
		}
	}

	return nil
}
