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

	"github.com/kcp-dev/logicalcluster/v2"

	rbacv1 "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metainternal "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/watch"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	rbacinformers "k8s.io/client-go/informers/rbac/v1"
	"k8s.io/client-go/kubernetes"
	clientrest "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/util/sets"
	"k8s.io/kubernetes/pkg/printers"
	printerstorage "k8s.io/kubernetes/pkg/printers/storage"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/projection"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/softimpersonation"
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

	impersonatedkubeClusterClient func(user kuser.Info) (kubernetes.ClusterInterface, error)
	kubeClusterClient             kubernetes.ClusterInterface
	kcpClusterClient              kcpclientset.ClusterInterface

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
	return lclusterName.String() + "|" + indexValue
}

var _ rest.Lister = &REST{}
var _ rest.Watcher = &REST{}
var _ rest.Scoper = &REST{}
var _ rest.Creater = &REST{}
var _ rest.GracefulDeleter = &REST{}

// NewREST returns a RESTStorage object that will work against ClusterWorkspace resources in
// org workspaces, projecting them to the Workspace type.
func NewREST(
	cfg *clientrest.Config,
	rootTenancyClient tenancyclient.TenancyV1alpha1Interface,
	kubeClusterClient kubernetes.ClusterInterface,
	kcpClusterClient kcpclientset.ClusterInterface,
	clusterWorkspaceCache *workspacecache.ClusterWorkspaceCache,
	wilcardsCRBInformer rbacinformers.ClusterRoleBindingInformer,
	getFilteredClusterWorkspaces func(orgClusterName logicalcluster.Name) FilteredClusterWorkspaces,
) *REST {
	mainRest := &REST{
		getFilteredClusterWorkspaces: getFilteredClusterWorkspaces,

		impersonatedkubeClusterClient: func(user kuser.Info) (kubernetes.ClusterInterface, error) {
			impersonatedConfig, err := softimpersonation.WithSoftImpersonatedConfig(cfg, user)
			if err != nil {
				return nil, err
			}
			return kubernetes.NewClusterForConfig(impersonatedConfig)
		},
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

// Destroy implements rest.Storage
func (s *REST) Destroy() {
	// Do nothing
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

func withoutGroupsWhenPersonal(user kuser.Info, usePersonalScope bool) kuser.Info {
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

func (s *REST) deprecatedAuthorizeOrgForUser(ctx context.Context, orgClusterName logicalcluster.Name, user kuser.Info, verb string) error {
	// Root org access is implicit for every user. For non-root orgs, we need to check for
	// verb=access permissions against the clusterworkspaces/content of the ClusterWorkspace of
	// the org in the root.
	if orgClusterName == tenancyv1alpha1.RootCluster || sets.NewString(user.GetGroups()...).Has(kuser.SystemPrivilegedGroup) {
		return nil
	}

	parent, orgName := orgClusterName.Split()
	authz, err := s.delegatedAuthz(parent, s.kubeClusterClient)
	if err != nil {
		klog.Errorf("failed to get delegated authorizer for logical cluster %s", user.GetName(), parent)
		return kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), orgName, fmt.Errorf(authorization.WorkspaceAcccessNotPermittedReason))
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
		return kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), orgName, fmt.Errorf(authorization.WorkspaceAcccessNotPermittedReason))
	} else if decision != authorizer.DecisionAllow {
		klog.Errorf("user %q lacks (%s) clusterworkspaces/content %q permission for %q in %s: %s", user.GetName(), decisions[decision], verb, orgName, parent, reason)
		return kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), orgName, fmt.Errorf(authorization.WorkspaceAcccessNotPermittedReason))
	}

	return nil
}

func (s *REST) authorizeForUser(ctx context.Context, orgClusterName logicalcluster.Name, currentUser kuser.Info, verb string, resourceName string) error {
	if sets.NewString(currentUser.GetGroups()...).Has(kuser.SystemPrivilegedGroup) {
		return nil
	}

	// Maintain compatibility with the old permission model until we completel remove
	// the ability to have personal workspaces in top-level organizations
	if parent, _ := orgClusterName.Split(); parent == tenancyv1alpha1.RootCluster {
		switch verb {
		case "get", "watch", "list":
			return s.deprecatedAuthorizeOrgForUser(ctx, orgClusterName, currentUser, "access")
		case "create":
			return s.deprecatedAuthorizeOrgForUser(ctx, orgClusterName, currentUser, "member")
		case "delete":
			if err := s.deprecatedAuthorizeOrgForUser(ctx, orgClusterName, currentUser, "access"); err != nil {
				return err
			}
			// Let's fall through here:
			// if access to the current org workspace is granted (old permission model),
			// we still need to check the `delete` permission for the workspace we want to delete
		default:
			klog.Errorf("Verb %q not supported in the case of a workspace living in a top-level organization", verb)
			return kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), resourceName, fmt.Errorf("%q workspace %q in workspace %q is not allowed", verb, resourceName, orgClusterName))
		}
	}

	// We need to softly impersonate the name of the user here, because the user Home workspace
	// might be created on-the-fly when receiving the SAR call.
	// And this automatically creation of the Home workspace needs to be done with the right user.
	//
	// We call this "soft" impersonation in the sense that the whole user JSON is added as an
	// additional request header, that will be explicitly read by the Home Workspace handler,
	// instead of changing the real user before authorization as for "hard" impersonation.
	softlyImpersonatedSARClusterClient, err := s.impersonatedkubeClusterClient(currentUser)
	if err != nil {
		return err
	}

	// check for <verb> permission on the ClusterWorkspace workspace subresource for the <resourceName>
	authz, err := s.delegatedAuthz(orgClusterName, softlyImpersonatedSARClusterClient)
	if err != nil {
		klog.Errorf("failed to get delegated authorizer for logical cluster %s", currentUser, orgClusterName)
		return kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), resourceName, fmt.Errorf("%q workspace %q in workspace %q is not allowed", verb, resourceName, orgClusterName))
	}
	workspaceAttr := authorizer.AttributesRecord{
		User:            currentUser,
		Verb:            verb,
		APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      tenancyv1alpha1.SchemeGroupVersion.Version,
		Resource:        "clusterworkspaces",
		Subresource:     "workspace",
		Name:            resourceName,
		ResourceRequest: true,
	}
	if decision, reason, err := authz.Authorize(ctx, workspaceAttr); err != nil {
		klog.Errorf("failed to authorize user %q to %q clusterworkspaces/workspace name %q in %s", currentUser.GetName(), verb, resourceName, orgClusterName)
		return kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), "", fmt.Errorf("%q workspace %q in workspace %q is not allowed", verb, resourceName, orgClusterName))
	} else if decision != authorizer.DecisionAllow {
		klog.Errorf("user %q lacks (%s) clusterworkspaces/workspace %s permission for %q in %s: %s", currentUser.GetName(), decisions[decision], verb, resourceName, orgClusterName, reason)
		return kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), resourceName, fmt.Errorf("%q workspace %q in workspace %q is not allowed", verb, resourceName, orgClusterName))
	}

	return nil
}

func shouldUsePersonalScope(scope string, orgClusterName logicalcluster.Name) bool {
	parent, _ := orgClusterName.Split()

	// Let's use personal scopes only in the old case when personal user workspaces used to be created
	// in top-level organizations
	return scope == PersonalScope && orgClusterName != tenancyv1alpha1.RootCluster && parent == tenancyv1alpha1.RootCluster
}

// List retrieves a list of Workspaces that match label.
func (s *REST) List(ctx context.Context, options *metainternal.ListOptions) (runtime.Object, error) {
	userInfo, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), "", fmt.Errorf("unable to list workspaces without a user on the context"))
	}
	orgClusterName := ctx.Value(WorkspacesOrgKey).(logicalcluster.Name)
	if err := s.authorizeForUser(ctx, orgClusterName, userInfo, "list", ""); err != nil {
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
	if err := s.authorizeForUser(ctx, orgClusterName, userInfo, "watch", ""); err != nil {
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

	usePersonalScope := shouldUsePersonalScope(ctx.Value(WorkspacesScopeKey).(string), orgClusterName)

	if usePersonalScope {
		internalName, err := s.getInternalNameFromPrettyName(userInfo, orgClusterName, name)
		if err != nil {
			return nil, err
		}
		name = internalName
	}

	if err := s.authorizeForUser(ctx, orgClusterName, userInfo, "get", name); err != nil {
		return nil, err
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
		if ws.Name == workspace.Name && logicalcluster.From(&ws).String() == logicalcluster.From(workspace).String() {
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
	workspace, isWorkspace := obj.(*tenancyv1beta1.Workspace)
	if !isWorkspace {
		return nil, kerrors.NewInvalid(tenancyv1beta1.SchemeGroupVersion.WithKind("Workspace").GroupKind(), obj.GetObjectKind().GroupVersionKind().String(), []*field.Error{})
	}

	var zero int64
	userInfo, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), "", fmt.Errorf("unable to create a workspace without a user on the context"))
	}

	orgClusterName := ctx.Value(WorkspacesOrgKey).(logicalcluster.Name)
	if err := s.authorizeForUser(ctx, orgClusterName, userInfo, "create", workspace.Name); err != nil {
		return nil, err
	}

	if scope := ctx.Value(WorkspacesScopeKey); scope != PersonalScope {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), "", fmt.Errorf("creating a workspace is only possible in the personal workspaces scope for now"))
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
		_ = s.kubeClusterClient.Cluster(orgClusterName).RbacV1().ClusterRoleBindings().Delete(ctx, clusterRoleBinding.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero})
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

	if err := s.authorizeForUser(ctx, orgClusterName, userInfo, "delete", internalName); err != nil {
		return nil, false, err
	}

	errorToReturn := s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, internalName, *options)
	if errorToReturn != nil && !kerrors.IsNotFound(errorToReturn) {
		return nil, false, errorToReturn
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
