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
	"strings"

	"github.com/kcp-dev/logicalcluster"

	rbacv1 "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/authentication/user"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	rbacinformers "k8s.io/client-go/informers/rbac/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/util/sets"
	"k8s.io/kubernetes/pkg/printers"
	printerstorage "k8s.io/kubernetes/pkg/printers/storage"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/projection"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/tenancy/v1alpha1"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/authorization"
	workspacecache "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cache"
	workspaceprinters "github.com/kcp-dev/kcp/pkg/virtual/workspaces/printers"
	workspaceutil "github.com/kcp-dev/kcp/pkg/virtual/workspaces/util"
)

const (
	OrganizationScope string = "all"
	PersonalScope     string = "personal"
	PrettyNameLabel   string = "workspaces.kcp.dev/pretty-name"
	InternalNameLabel string = "workspaces.kcp.dev/internal-name"
	PrettyNameIndex   string = "workspace-pretty-name"
	InternalNameIndex string = "workspace-internal-name"
)

// FilteredClusterWorkspaces allows to list and watch ClusterWorkspaces
// filtered by authorizaation, i.e. a user only sees those object he has access to.
type FilteredClusterWorkspaces interface {
	workspaceauth.Lister
	workspaceauth.WatchableCache
	AddWatcher(watcher workspaceauth.CacheWatcher)
	Stop()
}

var ScopeSet sets.String = sets.NewString(PersonalScope, OrganizationScope)

type WorkspacesScopeKeyType string

const (
	WorkspacesScopeKey WorkspacesScopeKeyType = "VirtualWorkspaceWorkspacesScope"
	WorkspacesOrgKey   WorkspacesScopeKeyType = "VirtualWorkspaceWorkspacesOrg"
)

type REST struct {
	// getFilteredClusterWorkspaces returns a provider for ClusterWorkspaces.
	getFilteredClusterWorkspaces func(orgClusterName logicalcluster.Name) FilteredClusterWorkspaces

	// crbInformer allows listing or searching for RBAC cluster role bindings through all orgs
	crbInformer rbacinformers.ClusterRoleBindingInformer

	kubeClusterClient kubernetes.ClusterInterface
	kcpClusterClient  kcpclientset.ClusterInterface

	// clusterWorkspaceCache is a global cache of cluster workspaces (for all orgs) used by the watcher.
	clusterWorkspaceCache *workspacecache.ClusterWorkspaceCache

	// delegatedAuthz implements cluster-aware SubjectAccessReview
	delegatedAuthz delegated.DelegatedAuthorizerFactory

	createStrategy rest.RESTCreateStrategy
	updateStrategy rest.RESTUpdateStrategy
	rest.TableConvertor
}

func AddNameIndexers(crbInformer rbacinformers.ClusterRoleBindingInformer) error {
	return crbInformer.Informer().AddIndexers(map[string]cache.IndexFunc{
		PrettyNameIndex: func(obj interface{}) ([]string, error) {
			if crb, isCRB := obj.(*rbacv1.ClusterRoleBinding); isCRB {
				return []string{lclusterAwareIndexValue(logicalcluster.From(crb), crb.Labels[PrettyNameLabel])}, nil
			}

			return []string{}, nil
		},
		InternalNameIndex: func(obj interface{}) ([]string, error) {
			if crb, isCRB := obj.(*rbacv1.ClusterRoleBinding); isCRB {
				return []string{lclusterAwareIndexValue(logicalcluster.From(crb), crb.Labels[InternalNameLabel])}, nil
			}

			return []string{}, nil
		},
	})
}

func lclusterAwareIndexValue(lclusterName logicalcluster.Name, indexValue string) string {
	return lclusterName.String() + "#$#" + indexValue
}

var _ rest.Lister = &REST{}
var _ rest.Watcher = &REST{}
var _ rest.Scoper = &REST{}
var _ rest.Creater = &REST{}
var _ rest.GracefulDeleter = &REST{}

// NewREST returns a RESTStorage object that will work against ClusterWorkspace resources in
// org workspaces, projecting them to the Workspace type.
func NewREST(
	rootTenancyClient tenancyclient.TenancyV1alpha1Interface,
	kubeClusterClient kubernetes.ClusterInterface,
	kcpClusterClient kcpclientset.ClusterInterface,
	clusterWorkspaceCache *workspacecache.ClusterWorkspaceCache,
	wilcardsCRBInformer rbacinformers.ClusterRoleBindingInformer,
	getFilteredClusterWorkspaces func(orgClusterName logicalcluster.Name) FilteredClusterWorkspaces,
) *REST {
	mainRest := &REST{
		getFilteredClusterWorkspaces: getFilteredClusterWorkspaces,

		kubeClusterClient: kubeClusterClient,
		kcpClusterClient:  kcpClusterClient,
		delegatedAuthz:    delegated.NewDelegatedAuthorizer,

		crbInformer: wilcardsCRBInformer,

		clusterWorkspaceCache: clusterWorkspaceCache,

		createStrategy: Strategy,
		updateStrategy: Strategy,

		TableConvertor: printerstorage.TableConvertor{TableGenerator: printers.NewTableGenerator().With(workspaceprinters.AddWorkspacePrintHandlers)},
	}

	return mainRest
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

func (s *REST) getPrettyNameFromInternalName(user kuser.Info, orgClusterName logicalcluster.Name, internalName string) (string, error) {
	list, err := s.crbInformer.Informer().GetIndexer().ByIndex(InternalNameIndex, lclusterAwareIndexValue(orgClusterName, internalName))
	if err != nil {
		return "", err
	}
	for _, el := range list {
		if crb, isCRB := el.(*rbacv1.ClusterRoleBinding); isCRB &&
			len(crb.Subjects) == 1 && crb.Subjects[0].Name == user.GetName() {
			return crb.Labels[PrettyNameLabel], nil
		}
	}
	return "", kerrors.NewNotFound(tenancyv1beta1.Resource("workspaces"), internalName)
}

func (s *REST) getInternalNameFromPrettyName(user kuser.Info, orgClusterName logicalcluster.Name, prettyName string) (string, error) {
	list, err := s.crbInformer.Informer().GetIndexer().ByIndex(PrettyNameIndex, lclusterAwareIndexValue(orgClusterName, prettyName))
	if err != nil {
		return "", err
	}
	for _, el := range list {
		if crb, isCRB := el.(*rbacv1.ClusterRoleBinding); isCRB &&
			len(crb.Subjects) == 1 && crb.Subjects[0].Name == user.GetName() {
			return crb.Labels[InternalNameLabel], nil
		}
	}
	return "", kerrors.NewNotFound(tenancyv1beta1.Resource("workspaces"), prettyName)
}

func withoutGroupsWhenPersonal(user user.Info, usePersonalScope bool) user.Info {
	if usePersonalScope {
		return &kuser.DefaultInfo{
			Name:   user.GetName(),
			UID:    user.GetUID(),
			Groups: []string{},
			Extra:  user.GetExtra(),
		}
	}
	return user
}

func (s *REST) authorizeOrgForUser(ctx context.Context, orgClusterName logicalcluster.Name, user user.Info, verb string) error {
	// Root org access is implicit for every user. For non-root orgs, we need to check for
	// verb=access permissions against the clusterworkspaces/content of the ClusterWorkspace of
	// the org in the root.
	if orgClusterName == tenancyv1alpha1.RootCluster || sets.NewString(user.GetGroups()...).Has("system:masters") {
		return nil
	}

	parent, orgName := orgClusterName.Split()
	authz, err := s.delegatedAuthz(parent, s.kubeClusterClient)
	if err != nil {
		klog.Errorf("failed to get delegated authorizer for logical cluster %s", user.GetName(), parent)
		return kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), orgName, fmt.Errorf("%q workspace access not permitted", parent))
	}
	typeUseAttr := authorizer.AttributesRecord{
		User:            user,
		Verb:            verb,
		APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      tenancyv1alpha1.SchemeGroupVersion.Version,
		Resource:        "clusterworkspaces",
		Subresource:     "content",
		Name:            orgName,
		ResourceRequest: true,
	}
	if decision, reason, err := authz.Authorize(ctx, typeUseAttr); err != nil {
		klog.Errorf("failed to authorize user %q to %q clusterworkspaces/content name %q in %s", user.GetName(), verb, orgName, parent)
		return kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), orgName, fmt.Errorf("%q workspace access not permitted", parent))
	} else if decision != authorizer.DecisionAllow {
		klog.Errorf("user %q lacks (%s) clusterworkspaces/content %q permission for %q in %s: %s", user.GetName(), decisions[decision], verb, orgName, parent, reason)
		return kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), orgName, fmt.Errorf("%q workspace access not permitted", parent))
	}

	return nil
}

func shouldUsePersonalScope(scope string, orgClusterName logicalcluster.Name) bool {
	return scope == PersonalScope && orgClusterName != tenancyv1alpha1.RootCluster
}

// List retrieves a list of Workspaces that match label.
func (s *REST) List(ctx context.Context, options *metainternal.ListOptions) (runtime.Object, error) {
	userInfo, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), "", fmt.Errorf("unable to list workspaces without a user on the context"))
	}
	orgClusterName := ctx.Value(WorkspacesOrgKey).(logicalcluster.Name)
	if err := s.authorizeOrgForUser(ctx, orgClusterName, userInfo, "access"); err != nil {
		return nil, err
	}

	usePersonalScope := shouldUsePersonalScope(ctx.Value(WorkspacesScopeKey).(string), orgClusterName)
	clusterWorkspaceList := &tenancyv1alpha1.ClusterWorkspaceList{}
	if clusterWorkspaces := s.getFilteredClusterWorkspaces(orgClusterName); clusterWorkspaces != nil {
		// TODO:
		// The workspaceLister is informer driven, so it's important to note that the lister can be stale.
		// It breaks the API guarantees of lists.
		// To make it correct we have to know the latest RV of the org workspace shard,
		// and then wait for freshness relative to that RV of the lister.
		labelSelector, fieldSelector := InternalListOptionsToSelectors(options)
		var err error
		clusterWorkspaceList, err = clusterWorkspaces.List(withoutGroupsWhenPersonal(userInfo, usePersonalScope), labelSelector, fieldSelector)
		if err != nil {
			return nil, err
		}
	}

	if usePersonalScope {
		for i, workspace := range clusterWorkspaceList.Items {
			var err error
			clusterWorkspaceList.Items[i].Name, err = s.getPrettyNameFromInternalName(userInfo, orgClusterName, workspace.Name)
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

func (s *REST) Watch(ctx context.Context, options *metainternal.ListOptions) (watch.Interface, error) {
	userInfo, exists := apirequest.UserFrom(ctx)
	if !exists {
		return nil, fmt.Errorf("no user")
	}

	orgClusterName := ctx.Value(WorkspacesOrgKey).(logicalcluster.Name)
	if err := s.authorizeOrgForUser(ctx, orgClusterName, userInfo, "access"); err != nil {
		return nil, err
	}
	clusterWorkspaces := s.getFilteredClusterWorkspaces(orgClusterName)

	includeAllExistingProjects := (options != nil) && options.ResourceVersion == "0"

	m := workspaceutil.MatchWorkspace(InternalListOptionsToSelectors(options))
	watcher := workspaceauth.NewUserWorkspaceWatcher(userInfo, orgClusterName, s.clusterWorkspaceCache, clusterWorkspaces, includeAllExistingProjects, m)
	clusterWorkspaces.AddWatcher(watcher)

	go watcher.Watch()
	return watcher, nil
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

	userInfo, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), "", fmt.Errorf("unable to list workspaces without a user on the context"))
	}

	orgClusterName := ctx.Value(WorkspacesOrgKey).(logicalcluster.Name)
	if err := s.authorizeOrgForUser(ctx, orgClusterName, userInfo, "access"); err != nil {
		return nil, err
	}

	usePersonalScope := shouldUsePersonalScope(ctx.Value(WorkspacesScopeKey).(string), orgClusterName)

	if usePersonalScope {
		internalName, err := s.getInternalNameFromPrettyName(userInfo, orgClusterName, name)
		if err != nil {
			return nil, err
		}
		name = internalName
	}

	clusterWorkspaces := s.getFilteredClusterWorkspaces(orgClusterName)
	if clusterWorkspaces == nil {
		return nil, kerrors.NewNotFound(tenancyv1beta1.Resource("workspaces"), name)
	}
	workspace, err := s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, name, opts)
	if err != nil {
		return nil, err
	}

	// TODO:
	// Filtering by applying the lister operation might not be necessary anymore
	// when using a semi-delegated authorizer in the workspaces virtual workspace that would
	// delegate this authorization to the main KCP instance hosting the workspaces and RBAC rules
	obj, err := clusterWorkspaces.List(withoutGroupsWhenPersonal(userInfo, usePersonalScope), labels.Everything(), fields.Everything())
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
		return nil, kerrors.NewNotFound(tenancyv1beta1.Resource("workspaces"), name)
	}

	if usePersonalScope {
		existingClusterWorkspace.Name, err = s.getPrettyNameFromInternalName(userInfo, orgClusterName, existingClusterWorkspace.Name)
		if err != nil {
			return nil, err
		}
	}
	return existingClusterWorkspace, nil
}

type RoleType string

const (
	OwnerRoleType RoleType = "owner"
)

var roleRules = map[RoleType][]rbacv1.PolicyRule{
	OwnerRoleType: {
		{
			Verbs:     []string{"get", "delete"},
			Resources: []string{"clusterworkspaces/workspace"},
		},
		{
			Resources: []string{"clusterworkspaces/content"},
			Verbs:     []string{"admin", "access"},
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
// However, when the user manages his workspaces through the personal scope, the pretty names will always be used.
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
	userInfo, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), "", fmt.Errorf("unable to create a workspace without a user on the context"))
	}

	orgClusterName := ctx.Value(WorkspacesOrgKey).(logicalcluster.Name)
	if err := s.authorizeOrgForUser(ctx, orgClusterName, userInfo, "member"); err != nil {
		return nil, err
	}

	if scope := ctx.Value(WorkspacesScopeKey); scope != PersonalScope {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), "", fmt.Errorf("creating a workspace is only possible in the personal workspaces scope for now"))
	}

	workspace, isWorkspace := obj.(*tenancyv1beta1.Workspace)
	if !isWorkspace {
		return nil, kerrors.NewInvalid(tenancyv1beta1.SchemeGroupVersion.WithKind("Workspace").GroupKind(), obj.GetObjectKind().GroupVersionKind().String(), []*field.Error{})
	}

	// check whether the user is allowed to use the cluster workspace type
	authz, err := s.delegatedAuthz(orgClusterName, s.kubeClusterClient)
	if err != nil {
		klog.Errorf("failed to get delegated authorizer for logical cluster %s", userInfo.GetName(), orgClusterName)
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), "", fmt.Errorf("use of the cluster workspace type %q in workspace %q is not allowed", workspace.Spec.Type, orgClusterName))
	}
	typeName := strings.ToLower(workspace.Spec.Type)
	if len(typeName) == 0 {
		typeName = "universal"
	}
	typeUseAttr := authorizer.AttributesRecord{
		User:            userInfo,
		Verb:            "use",
		APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      tenancyv1alpha1.SchemeGroupVersion.Version,
		Resource:        "clusterworkspacetypes",
		Name:            typeName,
		ResourceRequest: true,
	}
	if decision, reason, err := authz.Authorize(ctx, typeUseAttr); err != nil {
		klog.Errorf("failed to authorize user %q to %q clusterworkspacetypes name %q in %s", userInfo.GetName(), "use", typeName, orgClusterName)
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), workspace.Name, fmt.Errorf("use of the cluster workspace type %q in workspace %q is not allowed", workspace.Spec.Type, orgClusterName))
	} else if decision != authorizer.DecisionAllow {
		klog.Errorf("user %q lacks (%s) clusterworkspacetypes %q permission for %q in %s: %s", userInfo.GetName(), decisions[decision], "use", typeName, orgClusterName, reason)
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), workspace.Name, fmt.Errorf("use of the cluster workspace type %q in workspace %q is not allowed", workspace.Spec.Type, orgClusterName))
	}

	ownerRoleBindingName := getRoleBindingName(OwnerRoleType, workspace.Name, userInfo)

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
				Name:      userInfo.GetName(),
				Namespace: "",
			},
		},
	}
	if _, err := s.kubeClusterClient.Cluster(orgClusterName).RbacV1().ClusterRoleBindings().Create(ctx, &clusterRoleBinding, metav1.CreateOptions{}); err != nil {
		if kerrors.IsAlreadyExists(err) {
			return nil, kerrors.NewAlreadyExists(tenancyv1beta1.Resource("workspaces"), workspace.Name)
		}
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), workspace.Name, err)
	}

	// Then create the owner role related to the given workspace.
	// Note that ResourceNames contains the workspace pretty name for now.
	// It will be updated later on when the internal name of the workspace is known.
	ownerClusterRole := createClusterRole(ownerRoleBindingName, workspace.Name, OwnerRoleType)
	if _, err := s.kubeClusterClient.Cluster(orgClusterName).RbacV1().ClusterRoles().Create(ctx, ownerClusterRole, metav1.CreateOptions{}); err != nil && !kerrors.IsAlreadyExists(err) {
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
	createdClusterWorkspace, err := s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, clusterWorkspace, metav1.CreateOptions{})
	if err != nil && kerrors.IsAlreadyExists(err) {
		clusterWorkspace.Name = ""
		clusterWorkspace.GenerateName = workspace.Name + "-"
		createdClusterWorkspace, err = s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, clusterWorkspace, metav1.CreateOptions{})
	}
	if err != nil {
		_ = s.kubeClusterClient.Cluster(orgClusterName).RbacV1().ClusterRoles().Delete(ctx, ownerClusterRole.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		return nil, err
	}

	// Update the cluster roles with the new workspace internal name, and also
	// add the internal name as a label, to allow searching with it later on.
	for i := range ownerClusterRole.Rules {
		ownerClusterRole.Rules[i].ResourceNames = []string{createdClusterWorkspace.Name}
	}
	ownerClusterRole.Labels[InternalNameLabel] = createdClusterWorkspace.Name
	if _, err := s.kubeClusterClient.Cluster(orgClusterName).RbacV1().ClusterRoles().Update(ctx, ownerClusterRole, metav1.UpdateOptions{}); err != nil {
		_ = s.kubeClusterClient.Cluster(orgClusterName).RbacV1().ClusterRoles().Delete(ctx, ownerClusterRole.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
		_, _, _ = s.Delete(ctx, createdClusterWorkspace.Name, nil, &metav1.DeleteOptions{GracePeriodSeconds: &zero})
		if kerrors.IsConflict(err) {
			return nil, kerrors.NewConflict(tenancyv1beta1.Resource("workspaces"), workspace.Name, err)
		}
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), workspace.Name, err)
	}

	// Update the cluster role bindings with the new workspace internal and pretty names,
	// to allow searching with them later on.
	clusterRoleBinding.Labels[InternalNameLabel] = createdClusterWorkspace.Name
	clusterRoleBinding.Labels[PrettyNameLabel] = workspace.Name
	if _, err := s.kubeClusterClient.Cluster(orgClusterName).RbacV1().ClusterRoleBindings().Update(ctx, &clusterRoleBinding, metav1.UpdateOptions{}); err != nil {
		var zero int64
		_ = s.kubeClusterClient.Cluster(orgClusterName).RbacV1().ClusterRoleBindings().Delete(ctx, clusterRoleBinding.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
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
	createdWorkspace.Name = workspace.Name
	return &createdWorkspace, nil
}

var _ = rest.GracefulDeleter(&REST{})

func (s *REST) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	userInfo, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, false, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), name, fmt.Errorf("unable to delete a workspace without a user on the context"))
	}

	orgClusterName := ctx.Value(WorkspacesOrgKey).(logicalcluster.Name)
	if err := s.authorizeOrgForUser(ctx, orgClusterName, userInfo, "access"); err != nil {
		return nil, false, err
	}

	internalName := name
	scope := ctx.Value(WorkspacesScopeKey).(string)
	if usePersonalScope := shouldUsePersonalScope(scope, orgClusterName); usePersonalScope {
		var err error
		internalName, err = s.getInternalNameFromPrettyName(userInfo, orgClusterName, name)
		if err != nil {
			if kerrors.IsNotFound(err) {
				return nil, false, kerrors.NewNotFound(tenancyv1beta1.Resource("workspaces"), name)
			}
			return nil, false, err
		}
	}

	// check for delete permission on the ClusterWorkspace workspace subresource
	authz, err := s.delegatedAuthz(orgClusterName, s.kubeClusterClient)
	if err != nil {
		klog.Errorf("failed to get delegated authorizer for logical cluster %s", userInfo, orgClusterName)
		return nil, false, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), name, fmt.Errorf("deletion in workspace %q is not allowed", orgClusterName))
	}
	deleteWorkspaceAttr := authorizer.AttributesRecord{
		User:            userInfo,
		Verb:            "delete",
		APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      tenancyv1alpha1.SchemeGroupVersion.Version,
		Resource:        "clusterworkspaces",
		Subresource:     "workspace",
		Name:            internalName,
		ResourceRequest: true,
	}
	if decision, _, err := authz.Authorize(ctx, deleteWorkspaceAttr); err != nil {
		klog.Errorf("failed to authorize user %q to %q clusterworkspaces/workspace name %q in %s", userInfo.GetName(), "delete", internalName, orgClusterName)
		return nil, false, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), "", fmt.Errorf("deletion in workspace %q is not allowed", orgClusterName))
	} else if decision != authorizer.DecisionAllow {
		// check for admin verb on the content
		contentAdminAttr := authorizer.AttributesRecord{
			User:            userInfo,
			Verb:            "admin",
			APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
			APIVersion:      tenancyv1alpha1.SchemeGroupVersion.Version,
			Resource:        "clusterworkspaces",
			Subresource:     "content",
			Name:            internalName,
			ResourceRequest: true,
		}
		if decision, reason, err := authz.Authorize(ctx, contentAdminAttr); err != nil {
			klog.Errorf("failed to authorize user %q to %q clusterworkspaces/content name %q in %s", userInfo.GetName(), "admin", internalName, orgClusterName)
			return nil, false, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), "", fmt.Errorf("deletion in workspace %q is not allowed", orgClusterName))
		} else if decision != authorizer.DecisionAllow {
			klog.Errorf("user %q lacks (%s) clusterworkspaces/content %q permission and clusterworkspaces/workspace %s permission for %q in %s: %s", userInfo.GetName(), decisions[decision], "admin", "delete", internalName, orgClusterName, reason)
			return nil, false, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), internalName, fmt.Errorf("deletion in workspace %q is not allowed", orgClusterName))
		}
	}

	errorToReturn := s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, internalName, *options)
	if err != nil && !kerrors.IsNotFound(errorToReturn) {
		return nil, false, err
	}
	internalNameLabelSelector := fmt.Sprintf("%s=%s", InternalNameLabel, internalName)
	if err := s.kubeClusterClient.Cluster(orgClusterName).RbacV1().ClusterRoles().DeleteCollection(ctx, *options, metav1.ListOptions{
		LabelSelector: internalNameLabelSelector,
	}); err != nil {
		klog.Error(err)
	}
	if err := s.kubeClusterClient.Cluster(orgClusterName).RbacV1().ClusterRoleBindings().DeleteCollection(ctx, *options, metav1.ListOptions{
		LabelSelector: internalNameLabelSelector,
	}); err != nil {
		klog.Error(err)
	}

	return nil, false, errorToReturn
}

var decisions = map[authorizer.Decision]string{
	authorizer.DecisionAllow:     "allowed",
	authorizer.DecisionDeny:      "denied",
	authorizer.DecisionNoOpinion: "denied",
}
