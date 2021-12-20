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

package registry

import (
	"context"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	rbacv1Client "k8s.io/client-go/kubernetes/typed/rbac/v1"
	"k8s.io/kubernetes/pkg/printers"
	printerstorage "k8s.io/kubernetes/pkg/printers/storage"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workspaceClient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/tenancy/v1alpha1"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/auth"
	workspacecache "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cache"
	workspaceprinters "github.com/kcp-dev/kcp/pkg/virtual/workspaces/printers"
)

type REST struct {
	// rbacClient can modify RBAC rules
	rbacClient rbacv1Client.RbacV1Interface
	// workspaceClient can modify KCP workspaces
	workspaceClient workspaceClient.WorkspaceInterface
	// workspaceLister can enumerate workspace lists that enforce policy
	workspaceLister workspaceauth.Lister

	workspaceCache *workspacecache.WorkspaceCache

	// Allows extended behavior during creation, required
	createStrategy rest.RESTCreateStrategy
	// Allows extended behavior during updates, required
	updateStrategy rest.RESTUpdateStrategy

	rest.TableConvertor
}

var _ rest.Lister = &REST{}
var _ rest.Scoper = &REST{}
var _ rest.Creater = &REST{}

// NewREST returns a RESTStorage object that will work against Workspace resources
func NewREST(workspaceClient workspaceClient.WorkspaceInterface, rbacClient rbacv1Client.RbacV1Interface, workspaceLister workspaceauth.Lister, workspaceCache *workspacecache.WorkspaceCache) *REST {
	return &REST{
		rbacClient:      rbacClient,
		workspaceClient: workspaceClient,
		workspaceLister: workspaceLister,
		workspaceCache:  workspaceCache,
		createStrategy:  Strategy,
		updateStrategy:  Strategy,

		TableConvertor: printerstorage.TableConvertor{TableGenerator: printers.NewTableGenerator().With(workspaceprinters.AddWorkspacePrintHandlers)},
	}
}

// New returns a new Workspace
func (s *REST) New() runtime.Object {
	return &tenancyv1alpha1.Workspace{}
}

// NewList returns a new WorkspaceList
func (*REST) NewList() runtime.Object {
	return &tenancyv1alpha1.WorkspaceList{}
}

func (s *REST) NamespaceScoped() bool {
	return false
}

// List retrieves a list of Workspaces that match label.

func (s *REST) List(ctx context.Context, options *metainternal.ListOptions) (runtime.Object, error) {
	user, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspace"), "", fmt.Errorf("unable to list workspaces without a user on the context"))
	}

	// TODO: if personal scope => remove the groups from the cloned userInfo

	labelSelector, _ := InternalListOptionsToSelectors(options)
	workspaceList, err := s.workspaceLister.List(user, labelSelector)
	if err != nil {
		return nil, err
	}

	return workspaceList, nil
}

var _ = rest.Getter(&REST{})

// Get retrieves a Workspace by name
func (s *REST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	opts := metav1.GetOptions{}
	if options != nil {
		opts = *options
	}
	workspace, err := s.workspaceClient.Get(ctx, name, opts)
	if err != nil {
		return nil, err
	}

	// TODO Filtering by applying the List operation would probably not be necessary anymore
	// when using a semi-delegated authorizer in the workspaces virtual workspace that would
	// delegate this authorization to the main KCP instance hosting the workspaces and RBAC rules
	obj, err := s.List(ctx, &metainternal.ListOptions{
		ResourceVersion: options.ResourceVersion,
	})
	if err != nil {
		return nil, err
	}
	for _, ws := range obj.(*tenancyv1alpha1.WorkspaceList).Items {
		if ws.Name == workspace.Name && ws.ClusterName == workspace.ClusterName {
			return workspace, nil
		}
	}

	return nil, kerrors.NewNotFound(tenancyv1alpha1.SchemeGroupVersion.WithResource("workspaces").GroupResource(), name)
}

func InternalListOptionsToSelectors(options *metainternal.ListOptions) (labels.Selector, fields.Selector) {
	label := labels.Everything()
	if options != nil && options.LabelSelector != nil {
		label = options.LabelSelector
	}
	field := fields.Everything()
	if options != nil && options.FieldSelector != nil {
		field = options.FieldSelector
	}
	return label, field
}

var _ = rest.Creater(&REST{})

// Create creates a new version of a resource.
func (s *REST) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	user, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspace"), "", fmt.Errorf("unable to create a workspace without a user on the context"))
	}

	workspace, isWorkspace := obj.(*tenancyv1alpha1.Workspace)
	if !isWorkspace {
		return nil, kerrors.NewInvalid(tenancyv1alpha1.SchemeGroupVersion.WithKind("Workspace").GroupKind(), obj.GetObjectKind().GroupVersionKind().String(), []*field.Error{})
	}
	createdWorkspace, err := s.workspaceClient.Create(ctx, workspace, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}

	getRoleName := "get-workspace-" + workspace.Name
	getRoleBindingName := getRoleName + "-" + user.GetName()
	clusterRole := rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: getRoleName,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         []string{"get"},
				APIGroups:     []string{tenancyv1alpha1.SchemeGroupVersion.Group},
				Resources:     []string{"workspaces"},
				ResourceNames: []string{workspace.Name},
			},
		},
	}
	if _, err := s.rbacClient.ClusterRoles().Create(ctx, &clusterRole, metav1.CreateOptions{}); err != nil && !kerrors.IsAlreadyExists(err) {
		return nil, err
	}
	clusterRoleBinding := rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: getRoleBindingName,
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     getRoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "User",
				Name:      user.GetName(),
				Namespace: "",
			},
		},
	}
	if _, err := s.rbacClient.ClusterRoleBindings().Create(ctx, &clusterRoleBinding, metav1.CreateOptions{}); err != nil && !kerrors.IsAlreadyExists(err) {
		return nil, err
	}
	return createdWorkspace, nil
}
