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

package servicenamespace

import (
	"context"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kuberesources "github.com/kcp-dev/kcp/contrib/kube-bind/kubernetes/resources"
	kubebindv1alpha1 "github.com/kube-bind/kube-bind/pkg/apis/kubebind/v1alpha1"
)

type reconciler struct {
	scope kubebindv1alpha1.Scope

	getNamespace    func(name string) (*corev1.Namespace, error)
	createNamespace func(ctx context.Context, ns *corev1.Namespace) (*corev1.Namespace, error)
	deleteNamespace func(ctx context.Context, name string) error

	getRoleBinding    func(ns, name string) (*rbacv1.RoleBinding, error)
	createRoleBinding func(ctx context.Context, crb *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error)
	updateRoleBinding func(ctx context.Context, cr *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error)
}

func (c *reconciler) reconcile(ctx context.Context, sns *kubebindv1alpha1.APIServiceNamespace) error {
	var ns *corev1.Namespace
	nsName := sns.Namespace + "-" + sns.Name
	if sns.Status.Namespace != "" {
		nsName = sns.Status.Namespace
		ns, _ = c.getNamespace(nsName) // golint:errcheck
	}
	if ns == nil {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: nsName,
				Annotations: map[string]string{
					kubebindv1alpha1.APIServiceNamespaceAnnotationKey: sns.Namespace + "/" + sns.Name,
				},
			},
		}
		if _, err := c.createNamespace(ctx, ns); err != nil && !errors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create namespace %q: %w", nsName, err)
		}
	}

	if c.scope == kubebindv1alpha1.NamespacedScope {
		if err := c.ensureRBACRoleBinding(ctx, nsName, sns); err != nil {
			return fmt.Errorf("failed to ensure RBAC: %w", err)
		}
	}

	if sns.Status.Namespace != nsName {
		sns.Status.Namespace = nsName
	}

	return nil
}

func (c *reconciler) ensureRBACRoleBinding(ctx context.Context, ns string, sns *kubebindv1alpha1.APIServiceNamespace) error {
	objName := "kube-binder"
	binding, err := c.getRoleBinding(ns, objName)
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to get role binding %s/%s: %w", ns, objName, err)
	}

	expected := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      objName,
			Namespace: ns,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Namespace: sns.Namespace,
				Name:      kuberesources.ServiceAccountName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			Name:     "kube-binder-" + sns.Namespace,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	if binding == nil {
		if _, err := c.createRoleBinding(ctx, expected); err != nil {
			return fmt.Errorf("failed to create role binding %s/%s: %w", ns, objName, err)
		}
	} else if !reflect.DeepEqual(binding.Subjects, expected.Subjects) || !reflect.DeepEqual(binding.RoleRef, expected.RoleRef) {
		binding = binding.DeepCopy()
		binding.Subjects = expected.Subjects
		binding.RoleRef = expected.RoleRef
		if _, err := c.updateRoleBinding(ctx, binding); err != nil {
			return fmt.Errorf("failed to create role binding %s/%s: %w", ns, objName, err)
		}
	}

	return nil
}
