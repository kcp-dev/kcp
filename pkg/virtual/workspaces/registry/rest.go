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
	"k8s.io/apiserver/pkg/authentication/user"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	rbacinformers "k8s.io/client-go/informers/rbac/v1"
	"k8s.io/client-go/kubernetes"
	rbacv1client "k8s.io/client-go/kubernetes/typed/rbac/v1"
	rbacv1listers "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/util/sets"
	"k8s.io/kubernetes/pkg/printers"
	printerstorage "k8s.io/kubernetes/pkg/printers/storage"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/projection"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	tenancyclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/tenancy/v1alpha1"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/auth"
	workspaceprinters "github.com/kcp-dev/kcp/pkg/virtual/workspaces/printers"
)

const (
	OrganizationScope string = "organization"
	PersonalScope     string = "personal"
	PrettyNameLabel   string = "workspaces.kcp.dev/pretty-name"
	InternalNameLabel string = "workspaces.kcp.dev/internal-name"
	PrettyNameIndex   string = "workspace-pretty-name"
	InternalNameIndex string = "workspace-internal-name"
)

var ScopeSets sets.String = sets.NewString(PersonalScope, OrganizationScope)

type WorkspacesScopeKeyType string

const WorkspacesScopeKey WorkspacesScopeKeyType = "VirtualWorkspaceWorkspacesScope"

type REST struct {
	// rbacClient can modify RBAC rules
	rbacClient rbacv1client.RbacV1Interface
	// crbInformer allows listing or seaching for RBAC cluster role bindings
	crbInformer rbacinformers.ClusterRoleBindingInformer
	// crbLister allows listing RBAC cluster role bindings
	crbLister rbacv1listers.ClusterRoleBindingLister
	// clusterWorkspaceClient can modify KCP workspaces
	clusterWorkspaceClient tenancyclient.ClusterWorkspaceInterface
	// workspaceReviewerProvider allow getting a reviewer that checks
	// permissions for a given verb to workspaces
	workspaceReviewerProvider workspaceauth.ReviewerProvider
	// workspaceLister can enumerate workspace lists that enforce policy
	clusterWorkspaceLister workspaceauth.Lister

	// Allows extended behavior during creation, required
	createStrategy rest.RESTCreateStrategy
	// Allows extended behavior during updates, required
	updateStrategy rest.RESTUpdateStrategy

	rest.TableConvertor
}

func AddNameIndexers(crbInformer rbacinformers.ClusterRoleBindingInformer) error {
	return crbInformer.Informer().AddIndexers(map[string]cache.IndexFunc{
		PrettyNameIndex: func(obj interface{}) ([]string, error) {
			if crb, isCRB := obj.(*rbacv1.ClusterRoleBinding); isCRB {
				return []string{crb.Labels[PrettyNameLabel]}, nil
			}

			return []string{}, nil
		},
		InternalNameIndex: func(obj interface{}) ([]string, error) {
			if crb, isCRB := obj.(*rbacv1.ClusterRoleBinding); isCRB {
				return []string{crb.Labels[InternalNameLabel]}, nil
			}

			return []string{}, nil
		},
	})
}

var _ rest.Lister = &REST{}
var _ rest.Scoper = &REST{}
var _ rest.Creater = &REST{}
var _ rest.GracefulDeleter = &REST{}

// NewREST returns a RESTStorage object that will work against ClusterWorkspace resources in
// org workspaces, projecting them to the Workspace type.
func NewREST(tenancyClient tenancyclient.TenancyV1alpha1Interface, kubeClient kubernetes.Interface, crbInformer rbacinformers.ClusterRoleBindingInformer, workspaceReviewerProvider workspaceauth.ReviewerProvider, workspaceLister workspaceauth.Lister) (*REST, *KubeconfigSubresourceREST) {
	mainRest := &REST{
		rbacClient:                kubeClient.RbacV1(),
		crbInformer:               crbInformer,
		crbLister:                 crbInformer.Lister(),
		workspaceReviewerProvider: workspaceReviewerProvider,
		clusterWorkspaceClient:    tenancyClient.ClusterWorkspaces(),
		clusterWorkspaceLister:    workspaceLister,
		createStrategy:            Strategy,
		updateStrategy:            Strategy,

		TableConvertor: printerstorage.TableConvertor{TableGenerator: printers.NewTableGenerator().With(workspaceprinters.AddWorkspacePrintHandlers)},
	}
	return mainRest,
		&KubeconfigSubresourceREST{
			mainRest:             mainRest,
			coreClient:           kubeClient.CoreV1(),
			workspaceShardClient: tenancyClient.WorkspaceShards(),
		}
}

// New returns a new ClusterWorkspace
func (s *REST) New() runtime.Object {
	return &tenancyv1beta1.Workspace{}
}

// NewList returns a new ClusterWorkspaceList
func (*REST) NewList() runtime.Object {
	return &tenancyv1beta1.WorkspaceList{}
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
	return "", kerrors.NewNotFound(tenancyv1beta1.SchemeGroupVersion.WithResource("workspaces").GroupResource(), internalName)
}

func (s *REST) getInternalNameFromPrettyName(user kuser.Info, prettyName string) (string, error) {
	list, err := s.crbInformer.Informer().GetIndexer().ByIndex(PrettyNameIndex, prettyName)
	if err != nil {
		return "", err
	}
	for _, el := range list {
		if crb, isCRB := el.(*rbacv1.ClusterRoleBinding); isCRB &&
			len(crb.Subjects) == 1 && crb.Subjects[0].Name == user.GetName() {
			return crb.Labels[InternalNameLabel], nil
		}
	}
	return "", kerrors.NewNotFound(tenancyv1beta1.SchemeGroupVersion.WithResource("workspaces").GroupResource(), prettyName)
}

func withoutGroupsWhenPersonal(user user.Info, scope string) user.Info {
	if scope == PersonalScope {
		return &kuser.DefaultInfo{
			Name:   user.GetName(),
			UID:    user.GetUID(),
			Groups: []string{},
			Extra:  user.GetExtra(),
		}
	}
	return user
}

// List retrieves a list of Workspaces that match label.
func (s *REST) List(ctx context.Context, options *metainternal.ListOptions) (runtime.Object, error) {
	user, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspace"), "", fmt.Errorf("unable to list workspaces without a user on the context"))
	}

	scope := ctx.Value(WorkspacesScopeKey).(string)

	// TODO:
	// The workspaceLister is informer driven, so it's important to note that the lister can be stale.
	// It breaks the API guarantees of lists.
	// To make it correct we have to know the latest RV of the org workspace shard,
	// and then wait for freshness relative to that RV of the lister.
	labelSelector, _ := InternalListOptionsToSelectors(options)
	clusterWorkspaceList, err := s.clusterWorkspaceLister.List(withoutGroupsWhenPersonal(user, scope), labelSelector)
	if err != nil {
		return nil, err
	}

	if scope == PersonalScope {
		for i, workspace := range clusterWorkspaceList.Items {
			var err error
			clusterWorkspaceList.Items[i].Name, err = s.getPrettyNameFromInternalName(user, workspace.Name)
			if err != nil {
				return nil, err
			}
		}
	}

	workspaceList := &tenancyv1beta1.WorkspaceList{
		ListMeta: clusterWorkspaceList.ListMeta,
		Items:    make([]tenancyv1beta1.Workspace, len(clusterWorkspaceList.Items)),
	}

	for i, cws := range clusterWorkspaceList.Items {
		projection.ProjectClusterWorkspaceToWorkspace(&cws, &workspaceList.Items[i])
	}

	return workspaceList, nil
}

var _ = rest.Getter(&REST{})

// Get retrieves a Workspace by name
func (s *REST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	cws, err := s.getClusterWorkspace(ctx, name, options)
	if err != nil {
		return nil, err
	}

	var ws tenancyv1beta1.Workspace
	projection.ProjectClusterWorkspaceToWorkspace(cws, &ws)
	return &ws, nil
}

func (s *REST) getClusterWorkspace(ctx context.Context, name string, options *metav1.GetOptions) (*tenancyv1alpha1.ClusterWorkspace, error) {
	opts := metav1.GetOptions{}
	if options != nil {
		opts = *options
	}

	user, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspace"), "", fmt.Errorf("unable to list workspaces without a user on the context"))
	}

	scope := ctx.Value(WorkspacesScopeKey).(string)
	if scope == PersonalScope {
		internalName, err := s.getInternalNameFromPrettyName(user, name)
		if err != nil {
			return nil, err
		}
		name = internalName
	}

	workspace, err := s.clusterWorkspaceClient.Get(ctx, name, opts)
	if err != nil {
		return nil, err
	}

	// TODO:
	// Filtering by applying the lister operation might not be necessary anymore
	// when using a semi-delegated authorizer in the workspaces virtual workspace that would
	// delegate this authorization to the main KCP instance hosting the workspaces and RBAC rules
	obj, err := s.clusterWorkspaceLister.List(withoutGroupsWhenPersonal(user, scope), labels.Everything())
	if err != nil {
		return nil, err
	}
	var existingClusterWorkspace *tenancyv1alpha1.ClusterWorkspace
	for _, ws := range obj.Items {
		if ws.Name == workspace.Name && ws.ClusterName == workspace.ClusterName {
			existingClusterWorkspace = workspace
			break
		}
	}

	if existingClusterWorkspace == nil {
		return nil, kerrors.NewNotFound(tenancyv1beta1.SchemeGroupVersion.WithResource("workspaces").GroupResource(), name)
	}

	if scope == PersonalScope {
		existingClusterWorkspace.Name, err = s.getPrettyNameFromInternalName(user, existingClusterWorkspace.Name)
		if err != nil {
			return nil, err
		}
	}
	return existingClusterWorkspace, nil
}

type RoleType string

const (
	ListerRoleType RoleType = "lister"
	OwnerRoleType  RoleType = "owner"
)

var roleRules map[RoleType][]rbacv1.PolicyRule = map[RoleType][]rbacv1.PolicyRule{
	ListerRoleType: {
		{
			Verbs:     []string{"get"},
			Resources: []string{"workspaces"},
		},
		{
			Resources: []string{"workspaces/content"},
			Verbs:     []string{"view"},
		},
	},
	OwnerRoleType: {
		{
			Verbs:     []string{"get", "delete"},
			Resources: []string{"workspaces"},
		},
		{
			Resources: []string{"workspaces/content"},
			Verbs:     []string{"view", "edit"},
		},
	},
}

func createClusterRole(name, workspaceName string, roleType RoleType) *rbacv1.ClusterRole {
	var rules []rbacv1.PolicyRule
	for _, rule := range roleRules[roleType] {
		rule.APIGroups = []string{tenancyv1beta1.SchemeGroupVersion.Group}
		rule.ResourceNames = []string{workspaceName}
		rules = append(rules, rule)
	}
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{},
		},
		Rules: rules,
	}
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

// Create creates a new workspace
// The workspace is created in the underlying KCP server, with an internal name
// since the name ( == pretty name ) requested by the user might already exist at the organization level.
// Internal names would be <pretty name>--<suffix>.
//
// However when the user manages his workspaces through the personal scope, the pretty names will always be used.
//
// Personal pretty names and the related internal names are stored on the ClusterRoleBinding that links the
// ClusterWorkspace-related ClusterRole with the user Subject.
//
// Typical actions done against the underlying KCP instance when
//
//   kubectl create workspace my-app
//
// is issued by User-A against the virtual workspace at the personal scope:
//
//   1. create ClusterRoleBinding owner-workspace-my-app-user-A
//
// If this fails, then my-app already exists for the user A => conflict error.
//
//   2. create ClusterRoleBinding owner-workspace-my-app-user-A
//      create ClusterRole owner-workspace-my-app-user-A
//      create ClusterRole lister-workspace-my-app-user-A  (in order to later allow sharing)
//
//   3. create ClusterWorkspace my-app
//
// If this conflicts, create my-app--1, then my-app--2, …
//
//   4. update RoleBinding user-A-my-app to point to my-app-2 instead of my-app.
//
//   5. update ClusterRole owner-workspace-my-app-user-A to point to the internal workspace name
//      update the internalName and pretty annotation on cluster roles and cluster role bindings.
//
func (s *REST) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	var zero int64
	user, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspace"), "", fmt.Errorf("unable to create a workspace without a user on the context"))
	}

	if scope := ctx.Value(WorkspacesScopeKey); scope != PersonalScope {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspace"), "", fmt.Errorf("creating a workspace in only possible in the personal workspaces scope for now"))
	}

	workspace, isWorkspace := obj.(*tenancyv1beta1.Workspace)
	if !isWorkspace {
		return nil, kerrors.NewInvalid(tenancyv1beta1.SchemeGroupVersion.WithKind("Workspace").GroupKind(), obj.GetObjectKind().GroupVersionKind().String(), []*field.Error{})
	}
	ownerRoleBindingName := getRoleBindingName(OwnerRoleType, workspace.Name, user)
	listerRoleBindingName := getRoleBindingName(ListerRoleType, workspace.Name, user)

	// First create the ClusterRoleBinding that will link the workspace cluster role with the user Subject
	// This is created with a name unique inside the user personal scope (pretty name + userName),
	// So this automatically check for pretty name uniqueness in the user personal scope.
	clusterRoleBinding := rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ownerRoleBindingName,
			Labels: map[string]string{},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     ownerRoleBindingName,
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
			return nil, kerrors.NewAlreadyExists(tenancyv1beta1.Resource("workspaces"), workspace.Name)
		}
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), workspace.Name, err)
	}

	// Then create the owner and lister roles related to the given workspace.
	// Note that ResourceNames contains the workspace pretty name for now.
	// It will be updated later on when the internal name of the workspace is known.
	ownerClusterRole := createClusterRole(ownerRoleBindingName, workspace.Name, OwnerRoleType)
	if _, err := s.rbacClient.ClusterRoles().Create(ctx, ownerClusterRole, metav1.CreateOptions{}); err != nil && !kerrors.IsAlreadyExists(err) {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), workspace.Name, err)
	}

	listerClusterRole := createClusterRole(listerRoleBindingName, workspace.Name, ListerRoleType)
	if _, err := s.rbacClient.ClusterRoles().Create(ctx, listerClusterRole, metav1.CreateOptions{}); err != nil && !kerrors.IsAlreadyExists(err) {
		_ = s.rbacClient.ClusterRoles().Delete(ctx, ownerClusterRole.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), workspace.Name, err)
	}

	// Then try to create the workspace object itself, first with the pretty name,
	// retrying with increasing suffixes until a workspace with the same name
	// doesn't already exist.
	// The suffixed name based on the pretty name will be the internal name
	clusterWorkspace := &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: workspace.ObjectMeta,
		Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
			Type: workspace.Spec.Type,
		},
	}
	prettyName := workspace.Name
	var createdClusterWorkspace *tenancyv1alpha1.ClusterWorkspace
	var err error
	var nameSuffix string
	i := 0
	for i < 10 {
		if i > 0 {
			nameSuffix = fmt.Sprintf("-%d", i)
			clusterWorkspace.Name = fmt.Sprintf("%s-%s", prettyName, nameSuffix)
		}
		createdClusterWorkspace, err = s.clusterWorkspaceClient.Create(ctx, clusterWorkspace, metav1.CreateOptions{})
		if err == nil {
			break
		}
		if !kerrors.IsAlreadyExists(err) {
			return nil, err
		}
		i++
	}

	if err != nil {
		return nil, err
	}

	// Update the cluster roles with the new workspace internal name, and also
	// add the internal name as a label, to allow searching with it later on.
	for i := range ownerClusterRole.Rules {
		ownerClusterRole.Rules[i].ResourceNames = []string{createdClusterWorkspace.Name}
	}
	ownerClusterRole.Labels[InternalNameLabel] = createdClusterWorkspace.Name
	if _, err := s.rbacClient.ClusterRoles().Update(ctx, ownerClusterRole, metav1.UpdateOptions{}); err != nil {
		_ = s.rbacClient.ClusterRoles().Delete(ctx, ownerClusterRole.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		_ = s.rbacClient.ClusterRoles().Delete(ctx, listerClusterRole.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		_, _, _ = s.Delete(ctx, createdClusterWorkspace.Name, nil, &metav1.DeleteOptions{GracePeriodSeconds: &zero})
		if kerrors.IsConflict(err) {
			return nil, kerrors.NewConflict(tenancyv1beta1.Resource("workspaces"), workspace.Name, err)
		}
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), workspace.Name, err)
	}

	for i := range listerClusterRole.Rules {
		listerClusterRole.Rules[i].ResourceNames = []string{createdClusterWorkspace.Name}
	}
	listerClusterRole.Labels[InternalNameLabel] = createdClusterWorkspace.Name
	if _, err := s.rbacClient.ClusterRoles().Update(ctx, listerClusterRole, metav1.UpdateOptions{}); err != nil {
		_ = s.rbacClient.ClusterRoles().Delete(ctx, ownerClusterRole.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		_ = s.rbacClient.ClusterRoles().Delete(ctx, listerClusterRole.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		_, _, _ = s.Delete(ctx, createdClusterWorkspace.Name, nil, &metav1.DeleteOptions{GracePeriodSeconds: &zero})
		if kerrors.IsConflict(err) {
			return nil, kerrors.NewConflict(tenancyv1beta1.Resource("workspaces"), workspace.Name, err)
		}
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), workspace.Name, err)
	}

	// Update the cluster role bindings with the new workspace internal and pretty names,
	// to allow searching with them later on.
	clusterRoleBinding.Labels[InternalNameLabel] = createdClusterWorkspace.Name
	clusterRoleBinding.Labels[PrettyNameLabel] = prettyName
	if _, err := s.rbacClient.ClusterRoleBindings().Update(ctx, &clusterRoleBinding, metav1.UpdateOptions{}); err != nil {
		var zero int64
		_ = s.rbacClient.ClusterRoleBindings().Delete(ctx, clusterRoleBinding.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		_, _, _ = s.Delete(ctx, createdClusterWorkspace.Name, nil, &metav1.DeleteOptions{GracePeriodSeconds: &zero})
		if kerrors.IsConflict(err) {
			return nil, kerrors.NewConflict(tenancyv1beta1.Resource("workspaces"), workspace.Name, err)
		}
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), workspace.Name, err)
	}

	var createdWorkspace tenancyv1beta1.Workspace
	projection.ProjectClusterWorkspaceToWorkspace(createdClusterWorkspace, &createdWorkspace)

	// The workspace has been created with the internal name in KCP,
	// but will be returned to the user (in personal scope) with the pretty name.
	createdWorkspace.Name = prettyName
	return &createdWorkspace, nil
}

var _ = rest.GracefulDeleter(&REST{})

func (s *REST) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	user, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, false, kerrors.NewForbidden(tenancyv1beta1.Resource("workspace"), "", fmt.Errorf("unable to delete a workspace without a user on the context"))
	}

	internalName := name
	if scope := ctx.Value(WorkspacesScopeKey); scope == PersonalScope {
		var err error
		internalName, err = s.getInternalNameFromPrettyName(user, name)
		if err != nil {
			if kerrors.IsNotFound(err) {
				return nil, false, kerrors.NewNotFound(tenancyv1beta1.SchemeGroupVersion.WithResource("workspaces").GroupResource(), name)
			}
			return nil, false, err
		}
	}

	review, err := s.workspaceReviewerProvider.ForVerb("delete").Review(internalName)
	if err != nil {
		return nil, false, err
	}
	if review.EvaluationError() != "" {
		return nil, false, kerrors.NewForbidden(tenancyv1beta1.Resource("workspace"), "", errors.New(review.EvaluationError()))
	}
	if !sets.NewString(user.GetGroups()...).HasAny(review.Groups()...) &&
		!sets.NewString(review.Users()...).Has(user.GetName()) {
		return nil, false, kerrors.NewForbidden(tenancyv1beta1.Resource("workspace"), "", fmt.Errorf("User %s doesn't have the permission to delete workspace %s", user.GetName(), name))
	}

	errorToReturn := s.clusterWorkspaceClient.Delete(ctx, internalName, *options)
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
