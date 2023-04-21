/*
Copyright 2022 The KCP Authors.

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

package workspacetypeexists

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"
)

const (
	PluginName = "tenancy.kcp.io/WorkspaceTypeExists"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			plugin := &workspacetypeExists{
				Handler:          admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: delegated.NewDelegatedAuthorizer,
			}
			plugin.getType = func(path logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error) {
				return indexers.ByPathAndNameWithFallback[*tenancyv1alpha1.WorkspaceType](tenancyv1alpha1.Resource("workspacetypes"), plugin.typeIndexer, plugin.globalTypeIndexer, path, name)
			}
			plugin.transitiveTypeResolver = NewTransitiveTypeResolver(plugin.getType)

			return plugin, nil
		})
}

// workspacetypeExists does the following
//   - it checks existence of WorkspaceType in the same workspace,
//   - it applies the WorkspaceType initializers to the Workspace when it
//     transitions to the Initializing state.
type workspacetypeExists struct {
	*admission.Handler

	getType func(path logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error)

	typeIndexer       cache.Indexer
	globalTypeIndexer cache.Indexer

	logicalClusterLister corev1alpha1listers.LogicalClusterClusterLister

	transitiveTypeResolver TransitiveTypeResolver

	deepSARClient    kcpkubernetesclientset.ClusterInterface
	createAuthorizer delegated.DelegatedAuthorizerFactory
}

// Ensure that the required admission interfaces are implemented.
var (
	_ = admission.MutationInterface(&workspacetypeExists{})
	_ = admission.ValidationInterface(&workspacetypeExists{})
	_ = admission.InitializationValidator(&workspacetypeExists{})
	_ = kcpinitializers.WantsKcpInformers(&workspacetypeExists{})
	_ = kcpinitializers.WantsDeepSARClient(&workspacetypeExists{})
)

// Admit adds type initializer on transition to initializing phase.
func (o *workspacetypeExists) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("workspaces") {
		return nil
	}

	if a.GetObject().GetObjectKind().GroupVersionKind() != tenancyv1alpha1.SchemeGroupVersion.WithKind("Workspace") {
		return nil
	}
	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	ws := &tenancyv1alpha1.Workspace{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, ws); err != nil {
		return fmt.Errorf("failed to convert unstructured to Workspace: %w", err)
	}

	if !o.WaitForReady() {
		return admission.NewForbidden(a, fmt.Errorf("not yet ready to handle request"))
	}

	if a.GetOperation() != admission.Create {
		return nil
	}

	logicalCluster, err := o.logicalClusterLister.Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
	if err != nil {
		return admission.NewForbidden(a, fmt.Errorf("workspace type cannot be resolved: %w", err))
	}

	// if the user has not provided any type, use the default from the parent workspace
	empty := tenancyv1alpha1.WorkspaceTypeReference{}
	if ws.Spec.Type == empty {
		typeAnnotation, found := logicalCluster.Annotations[tenancyv1alpha1.LogicalClusterTypeAnnotationKey]
		if !found {
			return admission.NewForbidden(a, fmt.Errorf("annotation %s on LogicalCluster must be set", tenancyv1alpha1.LogicalClusterTypeAnnotationKey))
		}
		wtWorkspace, wtName := logicalcluster.NewPath(typeAnnotation).Split()
		if wtWorkspace.Empty() {
			return admission.NewForbidden(a, fmt.Errorf("annotation %s on LogicalCluster must be in the form of cluster:name", tenancyv1alpha1.LogicalClusterTypeAnnotationKey))
		}
		parentWt, err := o.getType(wtWorkspace, wtName)
		if err != nil {
			return admission.NewForbidden(a, fmt.Errorf("parent type cannot be resolved: %w", err))
		}
		if parentWt.Spec.DefaultChildWorkspaceType == nil {
			return admission.NewForbidden(a, errors.New("spec.defaultChildWorkspaceType of workspace type %s:%s must be set"))
		}
		ws.Spec.Type = tenancyv1alpha1.WorkspaceTypeReference{
			Path: parentWt.Spec.DefaultChildWorkspaceType.Path,
			Name: parentWt.Spec.DefaultChildWorkspaceType.Name,
		}
	}

	thisPath := logicalCluster.Annotations[core.LogicalClusterPathAnnotationKey]
	if thisPath == "" {
		thisPath = logicalcluster.From(logicalCluster).Path().String()
	}

	wt, err := o.resolveTypeRef(logicalcluster.NewPath(thisPath), tenancyv1alpha1.WorkspaceTypeReference{
		Path: ws.Spec.Type.Path,
		Name: ws.Spec.Type.Name,
	})
	if err != nil {
		return admission.NewForbidden(a, err)
	}
	if ws.Spec.Type.Path == "" {
		ws.Spec.Type.Path = canonicalPathFrom(wt).String()
	}

	addAdditionalWorkspaceLabels(wt, ws)

	return updateUnstructured(u, ws)
}

func (o *workspacetypeExists) resolveTypeRef(workspacePath logicalcluster.Path, ref tenancyv1alpha1.WorkspaceTypeReference) (*tenancyv1alpha1.WorkspaceType, error) {
	if ref.Path != "" {
		wt, err := o.getType(logicalcluster.NewPath(ref.Path), string(ref.Name))
		if err != nil {
			return nil, apierrors.NewInternalError(err)
		}

		return wt, err
	}

	for {
		wt, err := o.getType(workspacePath, string(ref.Name))
		if err != nil {
			if apierrors.IsNotFound(err) {
				parent, hasParent := workspacePath.Parent()
				if !hasParent && workspacePath != core.RootCluster.Path() {
					// fall through with root cluster. We always check types in there as last chance.
					parent = core.RootCluster.Path()
				} else if !hasParent {
					return nil, fmt.Errorf("workspace type %q cannot be resolved", ref.String())
				}
				workspacePath = parent
				continue
			}
			return nil, apierrors.NewInternalError(err)
		}
		return wt, err
	}
}

// Validate ensures that
// - has a valid type
// - has valid initializers when transitioning to initializing.
func (o *workspacetypeExists) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("workspaces") {
		return nil
	}

	if a.GetObject().GetObjectKind().GroupVersionKind() != tenancyv1alpha1.SchemeGroupVersion.WithKind("Workspace") {
		return nil
	}
	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	ws := &tenancyv1alpha1.Workspace{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, ws); err != nil {
		return fmt.Errorf("failed to convert unstructured to Workspace: %w", err)
	}

	switch a.GetOperation() {
	case admission.Update:
		if a.GetOldObject().GetObjectKind().GroupVersionKind() != tenancyv1alpha1.SchemeGroupVersion.WithKind("Workspace") {
			return nil
		}
		u, ok = a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetOldObject())
		}
		old := &tenancyv1alpha1.Workspace{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, old); err != nil {
			return fmt.Errorf("failed to convert unstructured to Workspace: %w", err)
		}

		if old.Spec.Type != ws.Spec.Type {
			return admission.NewForbidden(a, errors.New("spec.type is immutable"))
		}
	case admission.Create:
		if !o.WaitForReady() {
			return admission.NewForbidden(a, fmt.Errorf("not yet ready to handle request"))
		}

		wt, err := o.resolveTypeRef(clusterName.Path(), tenancyv1alpha1.WorkspaceTypeReference{
			Name: ws.Spec.Type.Name,
			Path: ws.Spec.Type.Path,
		})
		if err != nil {
			return admission.NewForbidden(a, err)
		}
		wtAliases, err := o.transitiveTypeResolver.Resolve(wt)
		if err != nil {
			return admission.NewForbidden(a, err)
		}

		if ws.Spec.Type.Path == "" {
			return admission.NewForbidden(a, fmt.Errorf("spec.type.path must be set"))
		}

		for _, alias := range wtAliases {
			authz, err := o.createAuthorizer(logicalcluster.From(alias), o.deepSARClient, delegated.Options{})
			if err != nil {
				return admission.NewForbidden(a, fmt.Errorf("unable to determine access to workspace type %q", ws.Spec.Type))
			}

			useAttr := authorizer.AttributesRecord{
				User:            a.GetUserInfo(),
				Verb:            "use",
				APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
				APIVersion:      tenancyv1alpha1.SchemeGroupVersion.Version,
				Resource:        "workspacetypes",
				Name:            alias.Name,
				ResourceRequest: true,
			}
			if decision, _, err := authz.Authorize(ctx, useAttr); err != nil {
				return admission.NewForbidden(a, fmt.Errorf("unable to determine access to workspace type %s:%s: %w", canonicalPathFrom(alias), alias.Name, err))
			} else if decision != authorizer.DecisionAllow {
				return admission.NewForbidden(a, fmt.Errorf("unable to use workspace type %s:%s: missing verb='use' permission on workspacetype", canonicalPathFrom(alias), alias.Name))
			}
		}

		// validate whether the workspace type is allowed in its parent, and the workspace type allows that parent
		logicalCluster, err := o.logicalClusterLister.Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
		if err != nil {
			return admission.NewForbidden(a, fmt.Errorf("workspace type cannot be resolved: %w", err))
		}
		typeAnnotation, found := logicalCluster.Annotations[tenancyv1alpha1.LogicalClusterTypeAnnotationKey]
		if !found {
			return admission.NewForbidden(a, fmt.Errorf("annotation %s on LogicalCluster must be set", tenancyv1alpha1.LogicalClusterTypeAnnotationKey))
		}
		wtWorkspace, wtName := logicalcluster.NewPath(typeAnnotation).Split()
		if wtWorkspace.Empty() {
			return admission.NewForbidden(a, fmt.Errorf("annotation %s on LogicalCluster must be in the form of cluster:name", tenancyv1alpha1.LogicalClusterTypeAnnotationKey))
		}
		parentWt, err := o.getType(wtWorkspace, wtName)
		if err != nil {
			return admission.NewForbidden(a, fmt.Errorf("workspace type cannot be resolved: %w", err))
		}
		parentAliases, err := o.transitiveTypeResolver.Resolve(parentWt)
		if err != nil {
			return admission.NewForbidden(a, err)
		}

		thisTypePath := wtWorkspace.Join(wtName)
		wTypeString := logicalcluster.NewPath(ws.Spec.Type.Path).Join(string(ws.Spec.Type.Name))
		if err := validateAllowedParents(parentAliases, wtAliases, thisTypePath, wTypeString); err != nil {
			return admission.NewForbidden(a, err)
		}
		if err := validateAllowedChildren(parentAliases, wtAliases, thisTypePath, wTypeString); err != nil {
			return admission.NewForbidden(a, err)
		}
	}

	return nil
}

func (o *workspacetypeExists) ValidateInitialization() error {
	if o.typeIndexer == nil {
		return fmt.Errorf(PluginName + " plugin needs a WorkspaceType indexer")
	}
	if o.globalTypeIndexer == nil {
		return fmt.Errorf(PluginName + " plugin needs a global WorkspaceType indexer")
	}
	if o.logicalClusterLister == nil {
		return fmt.Errorf(PluginName + " plugin needs a LogicalCluster lister")
	}
	return nil
}

func (o *workspacetypeExists) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	localTypesReady := local.Tenancy().V1alpha1().WorkspaceTypes().Informer().HasSynced
	globalTypesReady := global.Tenancy().V1alpha1().WorkspaceTypes().Informer().HasSynced

	logicalClusterReady := local.Core().V1alpha1().LogicalClusters().Informer().HasSynced

	o.SetReadyFunc(func() bool {
		return localTypesReady() && globalTypesReady() && logicalClusterReady()
	})

	o.typeIndexer = local.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer()
	o.globalTypeIndexer = global.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer()

	o.logicalClusterLister = local.Core().V1alpha1().LogicalClusters().Lister()

	indexers.AddIfNotPresentOrDie(local.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	indexers.AddIfNotPresentOrDie(global.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
}

func (o *workspacetypeExists) SetDeepSARClient(client kcpkubernetesclientset.ClusterInterface) {
	o.deepSARClient = client
}

// updateUnstructured updates the given unstructured object to match the given workspace.
func updateUnstructured(u *unstructured.Unstructured, ws *tenancyv1alpha1.Workspace) error {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ws)
	if err != nil {
		return err
	}
	u.Object = raw
	return nil
}

// addAdditionlWorkspaceLabels adds labels defined by the workspace
// type to the workspace if they are not already present.
func addAdditionalWorkspaceLabels(
	wt *tenancyv1alpha1.WorkspaceType,
	ws *tenancyv1alpha1.Workspace,
) {
	if len(wt.Spec.AdditionalWorkspaceLabels) > 0 {
		if ws.Labels == nil {
			ws.Labels = map[string]string{}
		}
		for key, value := range wt.Spec.AdditionalWorkspaceLabels {
			if _, ok := ws.Labels[key]; ok {
				// Do not override existing labels
				continue
			}
			ws.Labels[key] = value
		}
	}
}

// TODO: Move this out of admission to some shared location.
type TransitiveTypeResolver interface {
	Resolve(t *tenancyv1alpha1.WorkspaceType) ([]*tenancyv1alpha1.WorkspaceType, error)
}

type transitiveTypeResolver struct {
	getter func(cluster logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error)
}

func NewTransitiveTypeResolver(getter func(cluster logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error)) TransitiveTypeResolver {
	return &transitiveTypeResolver{
		getter: getter,
	}
}

// Resolve returns all WorkspaceTypes that a given Type extends.
func (r *transitiveTypeResolver) Resolve(t *tenancyv1alpha1.WorkspaceType) ([]*tenancyv1alpha1.WorkspaceType, error) {
	ret, err := r.resolve(t, map[string]bool{}, map[string]bool{}, []string{})
	if err != nil {
		return nil, err
	}
	return append(ret, t), nil
}

func (r *transitiveTypeResolver) resolve(wt *tenancyv1alpha1.WorkspaceType, seen, pathSeen map[string]bool, path []string) ([]*tenancyv1alpha1.WorkspaceType, error) {
	qualifiedName := canonicalPathFrom(wt).Join(wt.Name).String()
	seen[qualifiedName] = true
	pathSeen[qualifiedName] = true
	defer func() { pathSeen[qualifiedName] = false }()
	path = append(path, qualifiedName)

	ret := make([]*tenancyv1alpha1.WorkspaceType, 0, len(wt.Spec.Extend.With))
	for _, baseTypeRef := range wt.Spec.Extend.With {
		qualifiedName := logicalcluster.NewPath(baseTypeRef.Path).Join(tenancyv1alpha1.ObjectName(baseTypeRef.Name)).String()
		if pathSeen[qualifiedName] {
			// seen in this path already. That's a cycle.
			for i, t := range path {
				if t == qualifiedName {
					return nil, fmt.Errorf("circular dependency detected in workspace type %s: %s", qualifiedName, strings.Join(path[i:], " -> "))
				}
			}
			// should never happen
			return nil, fmt.Errorf("circular reference detected in workspace type %s", qualifiedName)
		}

		if seen[qualifiedName] {
			continue // already seen trunk
		}

		baseType, err := r.getter(logicalcluster.NewPath(baseTypeRef.Path), tenancyv1alpha1.ObjectName(baseTypeRef.Name))
		if err != nil {
			return nil, fmt.Errorf("unable to find inherited workspace type %s", qualifiedName)
		}
		ret = append(ret, baseType)

		parents, err := r.resolve(baseType, seen, pathSeen, path)
		if err != nil {
			return nil, err
		}
		ret = append(ret, parents...)
	}

	return ret, nil
}

func validateAllowedParents(parentAliases, childAliases []*tenancyv1alpha1.WorkspaceType, parentType, childType logicalcluster.Path) error {
	var errs []error
	for _, childAlias := range childAliases {
		if childAlias.Spec.LimitAllowedParents == nil {
			continue
		}
		if childAlias.Spec.LimitAllowedParents.None {
			errs = append(errs, fmt.Errorf("workspace type %s cannot have any parent", childType))
			continue
		}
		if len(childAlias.Spec.LimitAllowedParents.Types) == 0 {
			continue
		}

		qualifiedChild := canonicalPathFrom(childAlias).Join(string(tenancyv1alpha1.TypeName(childAlias.Name)))

		if !allOfTheFormerExistInTheLater(parentAliases, childAlias.Spec.LimitAllowedParents.Types) {
			allowedSet := sets.New[string]()
			for _, allowedParent := range childAlias.Spec.LimitAllowedParents.Types {
				allowedSet.Insert(logicalcluster.NewPath(allowedParent.Path).Join(string(allowedParent.Name)).String())
			}

			implementedSet := sets.New[string]()
			for _, parentAlias := range parentAliases {
				implementedSet.Insert(canonicalPathFrom(parentAlias).Join(string(tenancyv1alpha1.TypeName(parentAlias.Name))).String())
			}

			extending := ""
			if qualifiedChild != childType {
				extending = fmt.Sprintf(" extends %s, which", qualifiedChild)
			}

			errs = append(errs, fmt.Errorf("workspace type %s%s only allows %v parent workspaces, but parent type %s only implements %v",
				childType, extending, sets.List[string](allowedSet), parentType, sets.List[string](implementedSet)),
			)
		}
	}

	return utilerrors.NewAggregate(errs)
}

func validateAllowedChildren(parentAliases, childAliases []*tenancyv1alpha1.WorkspaceType, parentType, childType logicalcluster.Path) error {
	var errs []error
	for _, parentAlias := range parentAliases {
		if parentAlias.Spec.LimitAllowedChildren == nil {
			continue
		}
		if parentAlias.Spec.LimitAllowedChildren.None {
			errs = append(errs, fmt.Errorf("workspace type %s cannot have any child", parentType))
			continue
		}
		if len(parentAlias.Spec.LimitAllowedChildren.Types) == 0 {
			continue
		}

		qualifiedParent := canonicalPathFrom(parentAlias).Join(string(tenancyv1alpha1.TypeName(parentAlias.Name)))

		if !allOfTheFormerExistInTheLater(childAliases, parentAlias.Spec.LimitAllowedChildren.Types) {
			allowedSet := sets.New[string]()
			for _, allowedChild := range parentAlias.Spec.LimitAllowedChildren.Types {
				allowedSet.Insert(logicalcluster.NewPath(allowedChild.Path).Join(string(allowedChild.Name)).String())
			}

			implementedSet := sets.New[string]()
			for _, childAlias := range childAliases {
				implementedSet.Insert(canonicalPathFrom(childAlias).Join(string(tenancyv1alpha1.TypeName(childAlias.Name))).String())
			}

			extending := ""
			if qualifiedParent != parentType {
				extending = fmt.Sprintf(" extends %s, which", qualifiedParent)
			}

			errs = append(errs, fmt.Errorf("workspace type %s%s only allows %v child workspaces, but child type %s only implements %v",
				parentType, extending, sets.List[string](allowedSet), childType, sets.List[string](implementedSet)),
			)
		}
	}

	return utilerrors.NewAggregate(errs)
}

func allOfTheFormerExistInTheLater(objectAliases []*tenancyv1alpha1.WorkspaceType, allowedTypes []tenancyv1alpha1.WorkspaceTypeReference) bool {
	allowedAliasSet := sets.New[string]()
	for _, allowed := range allowedTypes {
		qualified := logicalcluster.NewPath(allowed.Path).Join(tenancyv1alpha1.ObjectName(allowed.Name)).String()
		allowedAliasSet.Insert(qualified)
	}

	for _, obj := range objectAliases {
		qualifiedObj := canonicalPathFrom(obj).Join(obj.Name).String()
		if allowedAliasSet.Has(qualifiedObj) {
			return true
		}
	}

	return false
}

func canonicalPathFrom(wt *tenancyv1alpha1.WorkspaceType) logicalcluster.Path {
	return logicalcluster.NewPath(wt.Annotations[core.LogicalClusterPathAnnotationKey])
}
