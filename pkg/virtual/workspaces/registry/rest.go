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

	kcprbacv1informers "github.com/kcp-dev/client-go/informers/rbac/v1"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
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
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/printers"
	printerstorage "k8s.io/kubernetes/pkg/printers/storage"

	clusterworkspaceadmission "github.com/kcp-dev/kcp/pkg/admission/clusterworkspace"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/projection"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	workspaceauth "github.com/kcp-dev/kcp/pkg/virtual/workspaces/authorization"
	workspacecache "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cache"
	workspaceprinters "github.com/kcp-dev/kcp/pkg/virtual/workspaces/printers"
	workspaceutil "github.com/kcp-dev/kcp/pkg/virtual/workspaces/util"
)

// FilteredClusterWorkspaces allows to list and watch ClusterWorkspaces
// filtered by authorizaation, i.e. a user only sees those object he has access to.
type FilteredClusterWorkspaces interface {
	workspaceauth.Lister
	workspaceauth.WatchableCache
	AddWatcher(watcher workspaceauth.CacheWatcher)
	Stop()
}

type WorkspacesScopeKeyType string

const (
	WorkspacesOrgKey WorkspacesScopeKeyType = "VirtualWorkspaceWorkspacesOrg"
)

type REST struct {
	// getFilteredClusterWorkspaces returns a provider for ClusterWorkspaces.
	getFilteredClusterWorkspaces func(orgClusterName logicalcluster.Name) FilteredClusterWorkspaces

	// crbInformer allows listing or searching for RBAC cluster role bindings through all orgs
	crbInformer kcprbacv1informers.ClusterRoleBindingClusterInformer

	kubeClusterClient kcpkubernetesclientset.ClusterInterface
	kcpClusterClient  kcpclientset.ClusterInterface

	// clusterWorkspaceCache is a global cache of cluster workspaces (for all orgs) used by the watcher.
	clusterWorkspaceCache *workspacecache.ClusterWorkspaceCache

	// delegatedAuthz implements cluster-aware SubjectAccessReview
	delegatedAuthz delegated.DelegatedAuthorizerFactory

	createStrategy rest.RESTCreateStrategy
	updateStrategy rest.RESTUpdateStrategy
	rest.TableConvertor
}

var _ rest.Lister = &REST{}
var _ rest.Watcher = &REST{}
var _ rest.Scoper = &REST{}
var _ rest.Creater = &REST{}
var _ rest.GracefulDeleter = &REST{}

// NewREST returns a RESTStorage object that will work against ClusterWorkspace resources in
// org workspaces, projecting them to the Workspace type.
func NewREST(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	kcpClusterClient kcpclientset.ClusterInterface,
	clusterWorkspaceCache *workspacecache.ClusterWorkspaceCache,
	wildcardsCRBInformer kcprbacv1informers.ClusterRoleBindingClusterInformer,
	getFilteredClusterWorkspaces func(orgClusterName logicalcluster.Name) FilteredClusterWorkspaces,
) *REST {
	mainRest := &REST{
		getFilteredClusterWorkspaces: getFilteredClusterWorkspaces,

		kubeClusterClient: kubeClusterClient,
		kcpClusterClient:  kcpClusterClient,
		delegatedAuthz:    delegated.NewDelegatedAuthorizer,

		crbInformer: wildcardsCRBInformer,

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

func (s *REST) canGetAll(ctx context.Context, cluster logicalcluster.Name, user kuser.Info) (bool, error) {
	clusterAuthorizer, err := s.delegatedAuthz(cluster, s.kubeClusterClient)
	if err != nil {
		return false, err
	}
	adminWorkspaceAttr := authorizer.AttributesRecord{
		User:            user,
		Verb:            "get",
		APIGroup:        tenancyv1beta1.SchemeGroupVersion.Group,
		APIVersion:      tenancyv1beta1.SchemeGroupVersion.Version,
		Resource:        "workspaces",
		Name:            "", // all
		ResourceRequest: true,
	}
	decision, _, err := clusterAuthorizer.Authorize(ctx, adminWorkspaceAttr)
	if err != nil {
		return false, err
	}
	return decision == authorizer.DecisionAllow, nil
}

// List retrieves a list of Workspaces that match label.
func (s *REST) List(ctx context.Context, options *metainternal.ListOptions) (runtime.Object, error) {
	userInfo, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), "", fmt.Errorf("unable to list workspaces without a user on the context"))
	}
	orgClusterName := ctx.Value(WorkspacesOrgKey).(logicalcluster.Name)

	clusterWorkspaceList := &tenancyv1alpha1.ClusterWorkspaceList{}
	if canGetAll, err := s.canGetAll(ctx, orgClusterName, userInfo); err != nil {
		return nil, err
	} else if canGetAll {
		v1Opts := metav1.ListOptions{}
		if err := metainternal.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1Opts, nil); err != nil {
			return nil, err
		}
		var err error
		clusterWorkspaceList, err = s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().List(ctx, v1Opts)
		if err != nil {
			return nil, err
		}
	} else if clusterWorkspaces := s.getFilteredClusterWorkspaces(orgClusterName); clusterWorkspaces != nil {
		// TODO:
		// The workspaceLister is informer driven, so it's important to note that the lister can be stale.
		// It breaks the API guarantees of lists.
		// To make it correct we have to know the latest RV of the org workspace shard,
		// and then wait for freshness relative to that RV of the lister.
		labelSelector, fieldSelector := InternalListOptionsToSelectors(options)
		var err error
		clusterWorkspaceList, err = clusterWorkspaces.List(userInfo, labelSelector, fieldSelector)
		if err != nil {
			return nil, err
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

	if canGetAll, err := s.canGetAll(ctx, orgClusterName, userInfo); err != nil {
		return nil, err
	} else if canGetAll {
		v1Opts := metav1.ListOptions{}
		if err := metainternal.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1Opts, nil); err != nil {
			return nil, err
		}
		w, err := s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Watch(ctx, v1Opts)
		if err != nil {
			return nil, err
		}
		return withProjection{delegate: w, ch: make(chan watch.Event)}, nil
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
	opts := metav1.GetOptions{}
	if options != nil {
		opts = *options
	}
	orgClusterName := ctx.Value(WorkspacesOrgKey).(logicalcluster.Name)
	cws, err := s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, name, opts)
	if err != nil {
		return nil, err
	}

	var ws tenancyv1beta1.Workspace
	projection.ProjectClusterWorkspaceToWorkspace(cws, &ws)
	return &ws, nil
}

var ownerRoleRules = []rbacv1.PolicyRule{
	{
		Verbs:     []string{"get", "delete"},
		Resources: []string{"workspaces"},
	},
	{
		Resources: []string{"workspaces/content"},
		Verbs:     []string{"admin", "access"},
	},
}

func getOwnerRoleBindingName(workspaceName string, user kuser.Info) string {
	return "owner-workspace-" + workspaceName + "-" + user.GetName()
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

// Create creates a new workspace.
// The corresponding ClusterWorkspace resource is created in the underlying KCP server.
//
// Workspace creation also creates a ClusterRole and a ClusterRoleBinding that links the
// ClusterRole with the user Subject.
//
// This will give the workspace creator the following permissions on the newly-created workspace:
// - 'cluster-admin' inside the newly created workspace,
// - 'get' permission to the workspace resource itself, so that it would appear when listing workspaces in the parent
// - 'delete' permission so that the user can delete a workspace it has created.
func (s *REST) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	workspace, isWorkspace := obj.(*tenancyv1beta1.Workspace)
	if !isWorkspace {
		return nil, kerrors.NewInvalid(tenancyv1beta1.SchemeGroupVersion.WithKind("Workspace").GroupKind(), obj.GetObjectKind().GroupVersionKind().String(), []*field.Error{})
	}

	userInfo, ok := apirequest.UserFrom(ctx)
	if !ok {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), "", fmt.Errorf("unable to create a workspace without a user on the context"))
	}

	orgClusterName := ctx.Value(WorkspacesOrgKey).(logicalcluster.Name)

	// Create the workspace object itself
	clusterWorkspace := &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: workspace.ObjectMeta,
		Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
			Type: workspace.Spec.Type,
		},
	}

	ownerRaw, err := clusterworkspaceadmission.ClusterWorkspaceOwnerAnnotationValue(userInfo)
	if err != nil {
		return nil, fmt.Errorf("error constructing workspace owner annotation from user info: %w", err)
	}

	if clusterWorkspace.Annotations == nil {
		clusterWorkspace.Annotations = make(map[string]string)
	}

	clusterWorkspace.Annotations[tenancyv1alpha1.ExperimentalWorkspaceOwnerAnnotationKey] = ownerRaw

	createdClusterWorkspace, err := s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, clusterWorkspace, metav1.CreateOptions{})
	if kerrors.IsAlreadyExists(err) {
		return nil, kerrors.NewAlreadyExists(tenancyv1beta1.Resource("workspaces"), workspace.Name)
	}
	if err != nil {
		return nil, err
	}
	var createdWorkspace tenancyv1beta1.Workspace
	projection.ProjectClusterWorkspaceToWorkspace(createdClusterWorkspace, &createdWorkspace)

	ownerRoleBindingName := getOwnerRoleBindingName(createdWorkspace.Name, userInfo)

	// First create the ClusterRoleBinding that will link the workspace cluster role with the user Subject
	clusterRoleBinding := rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: ownerRoleBindingName,
			Labels: map[string]string{
				tenancyv1beta1.WorkspaceNameLabel: createdWorkspace.Name,
			},
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
			return nil, kerrors.NewAlreadyExists(tenancyv1beta1.Resource("workspaces"), createdWorkspace.Name)
		}
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), createdWorkspace.Name, err)
	}

	// Then create the owner role related to the given workspace.
	var rules []rbacv1.PolicyRule
	for _, rule := range ownerRoleRules {
		rule.APIGroups = []string{tenancyv1beta1.SchemeGroupVersion.Group}
		rule.ResourceNames = []string{createdWorkspace.Name}
		rules = append(rules, rule)
	}
	ownerClusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: ownerRoleBindingName,
			Labels: map[string]string{
				tenancyv1beta1.WorkspaceNameLabel: createdWorkspace.Name,
			},
		},
		Rules: rules,
	}
	if _, err := s.kubeClusterClient.Cluster(orgClusterName).RbacV1().ClusterRoles().Create(ctx, ownerClusterRole, metav1.CreateOptions{}); err != nil && !kerrors.IsAlreadyExists(err) {
		return nil, kerrors.NewForbidden(tenancyv1beta1.Resource("workspaces"), createdWorkspace.Name, err)
	}

	return &createdWorkspace, nil
}

var _ = rest.GracefulDeleter(&REST{})

func (s *REST) Delete(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
	orgClusterName := ctx.Value(WorkspacesOrgKey).(logicalcluster.Name)
	logger := klog.FromContext(ctx).WithValues("parent", orgClusterName, "name", name)
	ctx = klog.NewContext(ctx, logger)

	err := s.kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, name, *options)
	if kerrors.IsNotFound(err) {
		err = kerrors.NewNotFound(tenancyv1beta1.Resource("workspaces"), name)
	}

	return nil, false, err
}

type withProjection struct {
	delegate watch.Interface
	ch       chan watch.Event
}

func (w withProjection) ResultChan() <-chan watch.Event {
	ch := w.delegate.ResultChan()

	go func() {
		defer close(w.ch)
		for ev := range ch {
			if ev.Object == nil {
				w.ch <- ev
				continue
			}
			if cws, ok := ev.Object.(*tenancyv1alpha1.ClusterWorkspace); ok {
				ws := &tenancyv1beta1.Workspace{}
				projection.ProjectClusterWorkspaceToWorkspace(cws, ws)
				ev.Object = ws
			}
			w.ch <- ev
		}
	}()

	return w.ch
}

func (w withProjection) Stop() {
	w.delegate.Stop()
}
