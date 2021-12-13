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
	"errors"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	rbacinformers "k8s.io/client-go/informers/rbac/v1"
	rbacv1client "k8s.io/client-go/kubernetes/typed/rbac/v1"
	rbacv1listers "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/util/sets"
	"k8s.io/kubernetes/pkg/printers"
	printerstorage "k8s.io/kubernetes/pkg/printers/storage"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workspaceClient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/tenancy/v1alpha1"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/auth"
	workspacecache "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cache"
	workspaceprinters "github.com/kcp-dev/kcp/pkg/virtual/workspaces/printers"
)

const (
	organizationScope string = "organization"
	personalScope     string = "personal"
	PrettyNameLabel   string = "workspaces.kcp.dev/pretty-name"
	InternalNameLabel string = "workspaces.kcp.dev/internal-name"
	PrettyNameIndex   string = "workspace-pretty-name"
	InternalNameIndex string = "workspace-internal-name"
)

var ScopeSets sets.String = sets.NewString(personalScope, organizationScope)

type WorkspacesScopeKeyType string

const WorkspacesScopeKey WorkspacesScopeKeyType = "VirtualWorkspaceWorkspacesScope"

type REST struct {
	// rbacClient can modify RBAC rules
	rbacClient rbacv1client.RbacV1Interface
	// crbInformer allows listing or seaching for RBAC cluster role bindings
	crbInformer rbacinformers.ClusterRoleBindingInformer
	// crbLister allows listing RBAC cluster role bindings
	crbLister rbacv1listers.ClusterRoleBindingLister
	// workspaceClient can modify KCP workspaces
	workspaceClient workspaceClient.WorkspaceInterface
	// workspaceReviewerProvider allow getting a reviewer that checks
	// permissions for a given verb to workspaces
	workspaceReviewerProvider workspaceauth.ReviewerProvider
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
var _ rest.GracefulDeleter = &REST{}

// NewREST returns a RESTStorage object that will work against Workspace resources
func NewREST(workspaceClient workspaceClient.WorkspaceInterface, rbacClient rbacv1client.RbacV1Interface, crbInformer rbacinformers.ClusterRoleBindingInformer, workspaceReviewerProvider workspaceauth.ReviewerProvider, workspaceLister workspaceauth.Lister, workspaceCache *workspacecache.WorkspaceCache) *REST {
	return &REST{
		rbacClient:                rbacClient,
		crbInformer:               crbInformer,
		crbLister:                 crbInformer.Lister(),
		workspaceReviewerProvider: workspaceReviewerProvider,
		workspaceClient:           workspaceClient,
		workspaceLister:           workspaceLister,
		workspaceCache:            workspaceCache,
		createStrategy:            Strategy,
		updateStrategy:            Strategy,

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

func (s *REST) getPrettyNameFromInternalName(user kuser.Info, internalName string) (string, error) {
	list, err := s.crbInformer.Informer().GetIndexer().ByIndex(InternalNameIndex, internalName)
	if err != nil {
		return "", err
	}
	for _, el := range list {
		if crb, isCRB := el.(*rbacv1.ClusterRoleBinding); isCRB &&
			len(crb.Subjects) == 1 && crb.Subjects[0].Name == user.GetName() {
			return crb.Labels[PrettyNameLabel], nil
		}
	}
	return "", nil
}

func (s *REST) getInternalNameFromPrettyName(user kuser.Info, prettyName string) (string, error) {
	roleBindingName := getRoleBindingName(AdminRoleType, prettyName, user)
	roleBinding, err := s.crbLister.Get(roleBindingName)
	if err != nil {
		return "", err
	}
	return roleBinding.Labels[InternalNameLabel], nil
}

// List retrieves a list of Workspaces that match label.
func (s *REST) List(ctx context.Context, options *metainternal.ListOptions) (runtime.Object, error) {
	user, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspace"), "", fmt.Errorf("unable to list workspaces without a user on the context"))
	}

	if scope := ctx.Value(WorkspacesScopeKey); scope == personalScope {
		user = &kuser.DefaultInfo{
			Name:   user.GetName(),
			UID:    user.GetUID(),
			Groups: []string{},
			Extra:  user.GetExtra(),
		}
	}

	labelSelector, _ := InternalListOptionsToSelectors(options)
	workspaceList, err := s.workspaceLister.List(user, labelSelector)
	if err != nil {
		return nil, err
	}

	if scope := ctx.Value(WorkspacesScopeKey); scope == personalScope {
		for i, workspace := range workspaceList.Items {
			var err error
			workspaceList.Items[i].Name, err = s.getPrettyNameFromInternalName(user, workspace.Name)
			if err != nil {
				return nil, err
			}
		}
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

	user, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspace"), "", fmt.Errorf("unable to list workspaces without a user on the context"))
	}

	workspace.Name, err = s.getPrettyNameFromInternalName(user, workspace.Name)
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

type RoleType string

const (
	ListerRoleType RoleType = "lister"
	AdminRoleType  RoleType = "admin"
)

var roleRules map[RoleType]rbacv1.PolicyRule = map[RoleType]rbacv1.PolicyRule{
	ListerRoleType: {
		Verbs:     []string{"get"},
		Resources: []string{"workspaces"},
	},
	AdminRoleType: {
		Verbs:     []string{"get", "delete"},
		Resources: []string{"workspaces"},
	},
}

func getRoleBindingName(roleType RoleType, workspacePrettyName string, user kuser.Info) string {
	return string(roleType) + "-workspace-" + workspacePrettyName + "-" + user.GetName()
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
	var zero int64
	user, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspace"), "", fmt.Errorf("unable to create a workspace without a user on the context"))
	}

	if scope := ctx.Value(WorkspacesScopeKey); scope != personalScope {
		return nil, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspace"), "", fmt.Errorf("Creating a workspace in only possible in the personal workspaces scope for now"))
	}

	workspace, isWorkspace := obj.(*tenancyv1alpha1.Workspace)
	if !isWorkspace {
		return nil, kerrors.NewInvalid(tenancyv1alpha1.SchemeGroupVersion.WithKind("Workspace").GroupKind(), obj.GetObjectKind().GroupVersionKind().String(), []*field.Error{})
	}

	adminRoleBindingName := getRoleBindingName(AdminRoleType, workspace.Name, user)
	listerRoleBindingName := getRoleBindingName(ListerRoleType, workspace.Name, user)
	clusterRoleBinding := rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   adminRoleBindingName,
			Labels: map[string]string{},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     adminRoleBindingName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "User",
				Name:      user.GetName(),
				Namespace: "",
			},
		},
	}
	if _, err := s.rbacClient.ClusterRoleBindings().Create(ctx, &clusterRoleBinding, metav1.CreateOptions{}); err != nil {
		if kerrors.IsAlreadyExists(err) {
			return nil, kerrors.NewAlreadyExists(tenancyv1alpha1.Resource("workspaces"), workspace.Name)
		}
		return nil, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspaces"), workspace.Name, err)
	}

	adminClusterRole := rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   adminRoleBindingName,
			Labels: map[string]string{},
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         roleRules[AdminRoleType].Verbs,
				APIGroups:     []string{tenancyv1alpha1.SchemeGroupVersion.Group},
				Resources:     roleRules[AdminRoleType].Resources,
				ResourceNames: []string{workspace.Name},
			},
		},
	}
	if _, err := s.rbacClient.ClusterRoles().Create(ctx, &adminClusterRole, metav1.CreateOptions{}); err != nil && !kerrors.IsAlreadyExists(err) {
		return nil, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspaces"), workspace.Name, err)
	}

	listerClusterRole := rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   listerRoleBindingName,
			Labels: map[string]string{},
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         roleRules[ListerRoleType].Verbs,
				APIGroups:     []string{tenancyv1alpha1.SchemeGroupVersion.Group},
				Resources:     roleRules[ListerRoleType].Resources,
				ResourceNames: []string{workspace.Name},
			},
		},
	}
	if _, err := s.rbacClient.ClusterRoles().Create(ctx, &listerClusterRole, metav1.CreateOptions{}); err != nil && !kerrors.IsAlreadyExists(err) {
		_ = s.rbacClient.ClusterRoles().Delete(ctx, adminClusterRole.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		return nil, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspaces"), workspace.Name, err)
	}

	prettyName := workspace.Name
	var createdWorkspace *tenancyv1alpha1.Workspace
	var err error
	var nameSuffix string
	i := 0
	for i < 10 {
		if i > 0 {
			nameSuffix = fmt.Sprintf("-%d", i)
			workspace.Name = fmt.Sprintf("%s-%s", prettyName, nameSuffix)
		}
		createdWorkspace, err = s.workspaceClient.Create(ctx, workspace, metav1.CreateOptions{})
		if err == nil {
			break
		}
		if !kerrors.IsAlreadyExists(err) {
			return nil, err
		}
		i++
	}

	adminClusterRole.Rules[0].ResourceNames = []string{createdWorkspace.Name}
	adminClusterRole.Labels[InternalNameLabel] = createdWorkspace.Name
	if _, err := s.rbacClient.ClusterRoles().Update(ctx, &adminClusterRole, metav1.UpdateOptions{}); err != nil {
		_ = s.rbacClient.ClusterRoles().Delete(ctx, adminClusterRole.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		_ = s.rbacClient.ClusterRoles().Delete(ctx, listerClusterRole.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		_, _, _ = s.Delete(ctx, createdWorkspace.Name, nil, &metav1.DeleteOptions{GracePeriodSeconds: &zero})
		if kerrors.IsConflict(err) {
			return nil, kerrors.NewConflict(tenancyv1alpha1.Resource("workspaces"), workspace.Name, err)
		}
		return nil, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspaces"), workspace.Name, err)
	}

	listerClusterRole.Rules[0].ResourceNames = []string{createdWorkspace.Name}
	listerClusterRole.Labels[InternalNameLabel] = createdWorkspace.Name
	if _, err := s.rbacClient.ClusterRoles().Update(ctx, &listerClusterRole, metav1.UpdateOptions{}); err != nil {
		_ = s.rbacClient.ClusterRoles().Delete(ctx, adminClusterRole.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		_ = s.rbacClient.ClusterRoles().Delete(ctx, listerClusterRole.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		_, _, _ = s.Delete(ctx, createdWorkspace.Name, nil, &metav1.DeleteOptions{GracePeriodSeconds: &zero})
		if kerrors.IsConflict(err) {
			return nil, kerrors.NewConflict(tenancyv1alpha1.Resource("workspaces"), workspace.Name, err)
		}
		return nil, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspaces"), workspace.Name, err)
	}

	clusterRoleBinding.Labels[InternalNameLabel] = createdWorkspace.Name
	clusterRoleBinding.Labels[PrettyNameLabel] = prettyName
	if _, err := s.rbacClient.ClusterRoleBindings().Update(ctx, &clusterRoleBinding, metav1.UpdateOptions{}); err != nil {
		var zero int64
		_ = s.rbacClient.ClusterRoleBindings().Delete(ctx, clusterRoleBinding.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		_, _, _ = s.Delete(ctx, createdWorkspace.Name, nil, &metav1.DeleteOptions{GracePeriodSeconds: &zero})
		if kerrors.IsConflict(err) {
			return nil, kerrors.NewConflict(tenancyv1alpha1.Resource("workspaces"), workspace.Name, err)
		}
		return nil, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspaces"), workspace.Name, err)
	}

	createdWorkspace.Name = prettyName
	return createdWorkspace, nil
}

var _ = rest.GracefulDeleter(&REST{})

func (s *REST) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	user, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, false, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspace"), "", fmt.Errorf("unable to create a workspace without a user on the context"))
	}

	internalName := name
	if scope := ctx.Value(WorkspacesScopeKey); scope == personalScope {
		var err error
		internalName, err = s.getInternalNameFromPrettyName(user, name)
		if err != nil {
			return nil, false, err
		}
	}

	review, err := s.workspaceReviewerProvider.ForVerb("delete").Review(internalName)
	if err != nil {
		return nil, false, err
	}
	if review.EvaluationError() != "" {
		return nil, false, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspace"), "", errors.New(review.EvaluationError()))
	}
	if !sets.NewString(user.GetGroups()...).HasAny(review.Groups()...) &&
		!sets.NewString(review.Users()...).Has(user.GetName()) {
		return nil, false, kerrors.NewForbidden(tenancyv1alpha1.Resource("workspace"), "", fmt.Errorf("User %s doesn't have the permission to delete workspace %s", user.GetName(), name))
	}

	errorToReturn := s.workspaceClient.Delete(ctx, internalName, *options)
	if err != nil && !kerrors.IsNotFound(errorToReturn) {
		return nil, false, err
	}
	internalNameLabelSelector := fmt.Sprintf("%s=%s", InternalNameLabel, internalName)
	if err := s.rbacClient.ClusterRoleBindings().DeleteCollection(ctx, *options, metav1.ListOptions{
		LabelSelector: internalNameLabelSelector,
	}); err != nil {
		klog.Error(err)
	}
	if err := s.rbacClient.ClusterRoles().DeleteCollection(ctx, *options, metav1.ListOptions{
		LabelSelector: internalNameLabelSelector,
	}); err != nil {
		klog.Error(err)
	}

	return nil, false, errorToReturn
}
