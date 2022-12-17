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
	"github.com/kcp-dev/kcp/pkg/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	corev1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/core/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

const (
	PluginName = "tenancy.kcp.dev/WorkspaceTypeExists"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			plugin := &workspacetypeExists{
				Handler:          admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: delegated.NewDelegatedAuthorizer,
			}
			plugin.getType = func(path logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error) {
				objs, err := plugin.typeIndexer.ByIndex(indexers.ByLogicalClusterPathAndName, path.Join(name).String())
				if err != nil {
					return nil, err
				}
				if len(objs) == 0 {
					return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("workspacetypes"), path.Join(name).String())
				}
				if len(objs) > 1 {
					return nil, fmt.Errorf("multiple WorkspaceTypes found for %s", path.Join(name).String())
				}
				return objs[0].(*tenancyv1alpha1.WorkspaceType), nil
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

	typeIndexer            cache.Indexer
	typeLister             tenancyv1alpha1listers.WorkspaceTypeClusterLister
	logicalClusterLister   corev1alpha1listers.LogicalClusterClusterLister
	deepSARClient          kcpkubernetesclientset.ClusterInterface
	transitiveTypeResolver TransitiveTypeResolver

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

	if a.GetResource().GroupResource() != tenancyv1beta1.Resource("workspaces") {
		return nil
	}

	if a.GetObject().GetObjectKind().GroupVersionKind() != tenancyv1beta1.SchemeGroupVersion.WithKind("Workspace") {
		return nil
	}
	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	ws := &tenancyv1beta1.Workspace{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, ws); err != nil {
		return fmt.Errorf("failed to convert unstructured to Workspace: %w", err)
	}

	if !o.WaitForReady() {
		return admission.NewForbidden(a, fmt.Errorf("not yet ready to handle request"))
	}

	if a.GetOperation() == admission.Create {
		this, err := o.logicalClusterLister.Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
		if err != nil {
			return admission.NewForbidden(a, fmt.Errorf("workspace type cannot be resolved: %w", err))
		}

		// if the user has not provided any type, use the default from the parent workspace
		empty := tenancyv1beta1.WorkspaceTypeReference{}
		if ws.Spec.Type == empty {
			typeAnnotation, found := this.Annotations[tenancyv1beta1.LogicalClusterTypeAnnotationKey]
			if !found {
				return admission.NewForbidden(a, fmt.Errorf("annotation %s on LogicalCluster must be set", tenancyv1beta1.LogicalClusterTypeAnnotationKey))
			}
			cwtWorkspace, cwtName := logicalcluster.NewPath(typeAnnotation).Split()
			if cwtWorkspace.Empty() {
				return admission.NewForbidden(a, fmt.Errorf("annotation %s on LogicalCluster must be in the form of cluster:name", tenancyv1beta1.LogicalClusterTypeAnnotationKey))
			}
			parentCwt, err := o.getType(cwtWorkspace, cwtName)
			if err != nil {
				return admission.NewForbidden(a, fmt.Errorf("parent type cannot be resolved: %w", err))
			}
			if parentCwt.Spec.DefaultChildWorkspaceType == nil {
				return admission.NewForbidden(a, errors.New("spec.defaultChildWorkspaceType of workspace type %s:%s must be set"))
			}
			ws.Spec.Type = tenancyv1beta1.WorkspaceTypeReference{
				Path: parentCwt.Spec.DefaultChildWorkspaceType.Path,
				Name: parentCwt.Spec.DefaultChildWorkspaceType.Name,
			}
		}

		thisPath := this.Annotations[core.LogicalClusterPathAnnotationKey]
		if thisPath == "" {
			thisPath = logicalcluster.From(this).Path().String()
		}

		cwt, err := o.resolveTypeRef(logicalcluster.NewPath(thisPath), tenancyv1alpha1.WorkspaceTypeReference{
			Path: ws.Spec.Type.Path,
			Name: ws.Spec.Type.Name,
		})
		if err != nil {
			return admission.NewForbidden(a, err)
		}
		if ws.Spec.Type.Path == "" {
			ws.Spec.Type.Path = canonicalPathFrom(cwt).String()
		}

		addAdditionalWorkspaceLabels(cwt, ws)

		return updateUnstructured(u, ws)
	}

	if a.GetOperation() != admission.Update {
		return nil
	}

	if a.GetOldObject().GetObjectKind().GroupVersionKind() != tenancyv1beta1.SchemeGroupVersion.WithKind("Workspace") {
		return nil
	}
	oldU, ok := a.GetOldObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetOldObject())
	}
	old := &tenancyv1beta1.Workspace{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(oldU.Object, old); err != nil {
		return fmt.Errorf("failed to convert unstructured to Workspace: %w", err)
	}

	// we only admit at state transition to initializing
	transitioningToInitializing :=
		old.Status.Phase != corev1alpha1.LogicalClusterPhaseInitializing &&
			ws.Status.Phase == corev1alpha1.LogicalClusterPhaseInitializing
	if !transitioningToInitializing {
		return nil
	}

	return updateUnstructured(u, ws)
}

func (o *workspacetypeExists) resolveTypeRef(workspacePath logicalcluster.Path, ref tenancyv1alpha1.WorkspaceTypeReference) (*tenancyv1alpha1.WorkspaceType, error) {
	if ref.Path != "" {
		cwt, err := o.getType(logicalcluster.NewPath(ref.Path), string(ref.Name))
		if err != nil {
			return nil, apierrors.NewInternalError(err)
		}

		return cwt, err
	}

	for {
		cwt, err := o.getType(workspacePath, string(ref.Name))
		if err != nil {
			if apierrors.IsNotFound(err) {
				parent, hasParent := workspacePath.Parent()
				if !hasParent && workspacePath != tenancyv1alpha1.RootCluster.Path() {
					// fall through with root cluster. We always check types in there as last chance.
					parent = tenancyv1alpha1.RootCluster.Path()
				} else if !hasParent {
					return nil, fmt.Errorf("workspace type %q cannot be resolved", ref.String())
				}
				workspacePath = parent
				continue
			}
			return nil, apierrors.NewInternalError(err)
		}
		return cwt, err
	}
}

// Validate ensures that
// - has a valid type
// - has valid initializers when transitioning to initializing
func (o *workspacetypeExists) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	if a.GetResource().GroupResource() != tenancyv1beta1.Resource("workspaces") {
		return nil
	}

	if a.GetObject().GetObjectKind().GroupVersionKind() != tenancyv1beta1.SchemeGroupVersion.WithKind("Workspace") {
		return nil
	}
	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	cw := &tenancyv1beta1.Workspace{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cw); err != nil {
		return fmt.Errorf("failed to convert unstructured to Workspace: %w", err)
	}

	switch a.GetOperation() {
	case admission.Update:
		if a.GetOldObject().GetObjectKind().GroupVersionKind() != tenancyv1beta1.SchemeGroupVersion.WithKind("Workspace") {
			return nil
		}
		u, ok = a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetOldObject())
		}
		old := &tenancyv1beta1.Workspace{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, old); err != nil {
			return fmt.Errorf("failed to convert unstructured to Workspace: %w", err)
		}

		if old.Spec.Type != cw.Spec.Type {
			return admission.NewForbidden(a, errors.New("spec.type is immutable"))
		}
	case admission.Create:
		if !o.WaitForReady() {
			return admission.NewForbidden(a, fmt.Errorf("not yet ready to handle request"))
		}

		cwt, err := o.resolveTypeRef(clusterName.Path(), tenancyv1alpha1.WorkspaceTypeReference{
			Name: cw.Spec.Type.Name,
			Path: cw.Spec.Type.Path,
		})
		if err != nil {
			return admission.NewForbidden(a, err)
		}
		cwtAliases, err := o.transitiveTypeResolver.Resolve(cwt)
		if err != nil {
			return admission.NewForbidden(a, err)
		}

		if cw.Spec.Type.Path == "" {
			return admission.NewForbidden(a, fmt.Errorf("spec.type.path must be set"))
		}

		for _, alias := range cwtAliases {
			authz, err := o.createAuthorizer(logicalcluster.From(alias), o.deepSARClient)
			if err != nil {
				return admission.NewForbidden(a, fmt.Errorf("unable to determine access to cluster workspace type %q", cw.Spec.Type))
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
				return admission.NewForbidden(a, fmt.Errorf("unable to determine access to cluster workspace type %s:%s: %w", canonicalPathFrom(alias), alias.Name, err))
			} else if decision != authorizer.DecisionAllow {
				return admission.NewForbidden(a, fmt.Errorf("unable to use cluster workspace type %s:%s: missing verb='use' permission on workspacetype", canonicalPathFrom(alias), alias.Name))
			}
		}

		// validate whether the workspace type is allowed in its parent, and the workspace type allows that parent
		this, err := o.logicalClusterLister.Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
		if err != nil {
			return admission.NewForbidden(a, fmt.Errorf("workspace type cannot be resolved: %w", err))
		}
		typeAnnotation, found := this.Annotations[tenancyv1beta1.LogicalClusterTypeAnnotationKey]
		if !found {
			return admission.NewForbidden(a, fmt.Errorf("annotation %s on LogicalCluster must be set", tenancyv1beta1.LogicalClusterTypeAnnotationKey))
		}
		cwtWorkspace, cwtName := logicalcluster.NewPath(typeAnnotation).Split()
		if cwtWorkspace.Empty() {
			return admission.NewForbidden(a, fmt.Errorf("annotation %s on LogicalCluster must be in the form of cluster:name", tenancyv1beta1.LogicalClusterTypeAnnotationKey))
		}
		parentCwt, err := o.getType(cwtWorkspace, cwtName)
		if err != nil {
			return admission.NewForbidden(a, fmt.Errorf("workspace type cannot be resolved: %w", err))
		}
		parentAliases, err := o.transitiveTypeResolver.Resolve(parentCwt)
		if err != nil {
			return admission.NewForbidden(a, err)
		}

		thisTypePath := cwtWorkspace.Join(cwtName)
		cwTypeString := logicalcluster.NewPath(cw.Spec.Type.Path).Join(string(cw.Spec.Type.Name))
		if err := validateAllowedParents(parentAliases, cwtAliases, thisTypePath, cwTypeString); err != nil {
			return admission.NewForbidden(a, err)
		}
		if err := validateAllowedChildren(parentAliases, cwtAliases, thisTypePath, cwTypeString); err != nil {
			return admission.NewForbidden(a, err)
		}
	}

	return nil
}

func (o *workspacetypeExists) ValidateInitialization() error {
	if o.typeLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an WorkspaceType lister")
	}
	if o.logicalClusterLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an LogicalCluster lister")
	}
	return nil
}

func (o *workspacetypeExists) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	typesReady := informers.Tenancy().V1alpha1().WorkspaceTypes().Informer().HasSynced
	logicalClusterReady := informers.Core().V1alpha1().LogicalClusters().Informer().HasSynced
	o.SetReadyFunc(func() bool {
		return typesReady() && logicalClusterReady()
	})
	o.typeLister = informers.Tenancy().V1alpha1().WorkspaceTypes().Lister()
	o.typeIndexer = informers.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer()
	o.logicalClusterLister = informers.Core().V1alpha1().LogicalClusters().Lister()

	indexers.AddIfNotPresentOrDie(informers.Tenancy().V1alpha1().WorkspaceTypes().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
}

func (o *workspacetypeExists) SetDeepSARClient(client kcpkubernetesclientset.ClusterInterface) {
	o.deepSARClient = client
}

// updateUnstructured updates the given unstructured object to match the given cluster workspace.
func updateUnstructured(u *unstructured.Unstructured, cw *tenancyv1beta1.Workspace) error {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cw)
	if err != nil {
		return err
	}
	u.Object = raw
	return nil
}

// addAdditionlWorkspaceLabels adds labels defined by the workspace
// type to the workspace if they are not already present.
func addAdditionalWorkspaceLabels(
	cwt *tenancyv1alpha1.WorkspaceType,
	cw *tenancyv1beta1.Workspace,
) {
	if len(cwt.Spec.AdditionalWorkspaceLabels) > 0 {
		if cw.Labels == nil {
			cw.Labels = map[string]string{}
		}
		for key, value := range cwt.Spec.AdditionalWorkspaceLabels {
			if _, ok := cw.Labels[key]; ok {
				// Do not override existing labels
				continue
			}
			cw.Labels[key] = value
		}
	}
}

// TODO: Move this out of admission to some shared location
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

func (r *transitiveTypeResolver) resolve(cwt *tenancyv1alpha1.WorkspaceType, seen, pathSeen map[string]bool, path []string) ([]*tenancyv1alpha1.WorkspaceType, error) {
	qualifiedName := canonicalPathFrom(cwt).Join(cwt.Name).String()
	seen[qualifiedName] = true
	pathSeen[qualifiedName] = true
	defer func() { pathSeen[qualifiedName] = false }()
	path = append(path, qualifiedName)

	var ret []*tenancyv1alpha1.WorkspaceType
	for _, baseTypeRef := range cwt.Spec.Extend.With {
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
		if childAlias.Spec.LimitAllowedParents == nil || len(childAlias.Spec.LimitAllowedParents.Types) == 0 {
			continue
		}

		qualifiedChild := canonicalPathFrom(childAlias).Join(string(tenancyv1alpha1.TypeName(childAlias.Name)))

		if !allOfTheFormerExistInTheLater(parentAliases, childAlias.Spec.LimitAllowedParents.Types) {
			allowedSet := sets.NewString()
			for _, allowedParent := range childAlias.Spec.LimitAllowedParents.Types {
				allowedSet.Insert(logicalcluster.NewPath(allowedParent.Path).Join(string(allowedParent.Name)).String())
			}

			implementedSet := sets.NewString()
			for _, parentAlias := range parentAliases {
				implementedSet.Insert(canonicalPathFrom(parentAlias).Join(string(tenancyv1alpha1.TypeName(parentAlias.Name))).String())
			}

			extending := ""
			if qualifiedChild != childType {
				extending = fmt.Sprintf(" extends %s, which", qualifiedChild)
			}

			errs = append(errs, fmt.Errorf("workspace type %s%s only allows %v parent workspaces, but parent type %s only implements %v",
				childType, extending, allowedSet.List(), parentType, implementedSet.List()),
			)
		}
	}

	return utilerrors.NewAggregate(errs)
}

func validateAllowedChildren(parentAliases, childAliases []*tenancyv1alpha1.WorkspaceType, parentType, childType logicalcluster.Path) error {
	var errs []error
	for _, parentAlias := range parentAliases {
		if parentAlias.Spec.LimitAllowedChildren == nil || len(parentAlias.Spec.LimitAllowedChildren.Types) == 0 {
			continue
		}
		if parentAlias.Spec.LimitAllowedChildren.None {
			return fmt.Errorf("workspace type %s cannot have any children", parentType)
		}

		qualifiedParent := canonicalPathFrom(parentAlias).Join(string(tenancyv1alpha1.TypeName(parentAlias.Name)))

		if !allOfTheFormerExistInTheLater(childAliases, parentAlias.Spec.LimitAllowedChildren.Types) {
			allowedSet := sets.NewString()
			for _, allowedChild := range parentAlias.Spec.LimitAllowedChildren.Types {
				allowedSet.Insert(logicalcluster.NewPath(allowedChild.Path).Join(string(allowedChild.Name)).String())
			}

			implementedSet := sets.NewString()
			for _, childAlias := range childAliases {
				implementedSet.Insert(canonicalPathFrom(childAlias).Join(string(tenancyv1alpha1.TypeName(childAlias.Name))).String())
			}

			extending := ""
			if qualifiedParent != parentType {
				extending = fmt.Sprintf(" extends %s, which", qualifiedParent)
			}

			errs = append(errs, fmt.Errorf("workspace type %s%s only allows %v child workspaces, but child type %s only implements %v",
				parentType, extending, allowedSet.List(), childType, implementedSet.List()),
			)
		}
	}

	return utilerrors.NewAggregate(errs)
}

func allOfTheFormerExistInTheLater(objectAliases []*tenancyv1alpha1.WorkspaceType, allowedTypes []tenancyv1alpha1.WorkspaceTypeReference) bool {
	allowedAliasSet := sets.NewString()
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

func canonicalPathFrom(cwt *tenancyv1alpha1.WorkspaceType) logicalcluster.Path {
	return logicalcluster.NewPath(cwt.Annotations[core.LogicalClusterPathAnnotationKey])
}
