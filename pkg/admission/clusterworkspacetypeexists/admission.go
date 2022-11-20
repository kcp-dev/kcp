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

package clusterworkspacetypeexists

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	"github.com/kcp-dev/kcp/pkg/client"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

const (
	PluginName = "tenancy.kcp.dev/ClusterWorkspaceTypeExists"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			plugin := &clusterWorkspaceTypeExists{
				Handler:          admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: delegated.NewDelegatedAuthorizer,
			}
			plugin.transitiveTypeResolver = NewTransitiveTypeResolver(
				func(cluster logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
					return plugin.typeLister.Get(client.ToClusterAwareKey(cluster, name))
				},
			)

			return plugin, nil
		})
}

// clusterWorkspaceTypeExists does the following
//   - it checks existence of ClusterWorkspaceType in the same workspace,
//   - it applies the ClusterWorkspaceType initializers to the Workspace when it
//     transitions to the Initializing state.
type clusterWorkspaceTypeExists struct {
	*admission.Handler
	typeLister             tenancyv1alpha1listers.ClusterWorkspaceTypeLister
	thisWorkspaceLister    tenancyv1alpha1listers.ThisWorkspaceLister
	deepSARClient          kcpkubernetesclientset.ClusterInterface
	transitiveTypeResolver TransitiveTypeResolver

	createAuthorizer delegated.DelegatedAuthorizerFactory
}

// Ensure that the required admission interfaces are implemented.
var (
	_ = admission.MutationInterface(&clusterWorkspaceTypeExists{})
	_ = admission.ValidationInterface(&clusterWorkspaceTypeExists{})
	_ = admission.InitializationValidator(&clusterWorkspaceTypeExists{})
	_ = kcpinitializers.WantsKcpInformers(&clusterWorkspaceTypeExists{})
	_ = kcpinitializers.WantsDeepSARClient(&clusterWorkspaceTypeExists{})
)

// Admit adds type initializer on transition to initializing phase.
func (o *clusterWorkspaceTypeExists) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("workspaces") {
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
		// if the user has not provided any type, use the default from the parent workspace
		empty := tenancyv1alpha1.ResolvedWorkspaceTypeReference{}
		if ws.Spec.Type == empty {
			this, err := o.thisWorkspaceLister.Get(client.ToClusterAwareKey(clusterName, tenancyv1alpha1.ThisWorkspaceName))
			if err != nil {
				return admission.NewForbidden(a, fmt.Errorf("workspace type canot be resolved: %w", err))
			}
			typeAnnotation, found := this.Annotations[tenancyv1beta1.WorkspaceTypeThisWorkspaceAnnotationKey]
			if !found {
				return admission.NewForbidden(a, fmt.Errorf("annotation %s on ThisWorkspace must be set", tenancyv1beta1.WorkspaceTypeThisWorkspaceAnnotationKey))
			}
			cwtCluster, cwtName := logicalcluster.New(typeAnnotation).Split()
			if cwtCluster.Empty() {
				return admission.NewForbidden(a, fmt.Errorf("annotation %s on ThisWorkspace must be in the form of cluster:name", tenancyv1beta1.WorkspaceTypeThisWorkspaceAnnotationKey))
			}
			parentCwt, err := o.typeLister.Get(client.ToClusterAwareKey(cwtCluster, cwtName))
			if err != nil {
				return admission.NewForbidden(a, fmt.Errorf("parent type canot be resolved: %w", err))
			}
			if parentCwt.Spec.DefaultChildWorkspaceType == nil {
				return admission.NewForbidden(a, errors.New("spec.defaultChildWorkspaceType of workspace type %s:%s must be set"))
			}
			ws.Spec.Type = tenancyv1alpha1.ResolvedWorkspaceTypeReference{ClusterWorkspaceTypeReference: *parentCwt.Spec.DefaultChildWorkspaceType}
		}

		cwt, err := o.resolveTypeRef(tenancy.CanonicalPathFrom(ctx), ws.Spec.Type.ClusterWorkspaceTypeReference)
		if err != nil {
			return admission.NewForbidden(a, err)
		}
		ws.Spec.Type.Path = logicalcluster.From(cwt).String()
		ws.Spec.Type.Cluster = tenancy.Cluster(logicalcluster.From(cwt).String())

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
		old.Status.Phase != tenancyv1alpha1.WorkspacePhaseInitializing &&
			ws.Status.Phase == tenancyv1alpha1.WorkspacePhaseInitializing
	if !transitioningToInitializing {
		return nil
	}

	// add initializers from type and aliases to workspace
	if ws.Spec.Type.Cluster == "" {
		// Defaulting of old objects that pre-date the cluster field, but are not
		// initialized yet. Admittedly, there will not be many, but :shrug:
		ws.Spec.Type.Cluster = tenancy.Cluster(ws.Spec.Type.Path)
	}

	return updateUnstructured(u, ws)
}

func (o *clusterWorkspaceTypeExists) resolveTypeRef(workspacePath logicalcluster.Name, ref tenancyv1alpha1.ClusterWorkspaceTypeReference) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
	if ref.Path != "" {
		cluster := tenancy.TemporaryClusterFrom(logicalcluster.New(ref.Path))
		cwt, err := o.typeLister.Get(client.ToClusterAwareKey(cluster.LogicalCluster(), tenancyv1alpha1.ObjectName(ref.Name)))
		if err != nil {
			return nil, apierrors.NewInternalError(err)
		}

		return cwt, err
	}

	for {
		cluster := tenancy.TemporaryClusterFrom(workspacePath)
		cwt, err := o.typeLister.Get(client.ToClusterAwareKey(cluster.LogicalCluster(), tenancyv1alpha1.ObjectName(ref.Name)))
		if err != nil {
			if apierrors.IsNotFound(err) {
				var hasParent bool
				workspacePath, hasParent = workspacePath.Parent()
				if !hasParent {
					return nil, fmt.Errorf("workspace type %s does not exist", ref.String())
				}
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
func (o *clusterWorkspaceTypeExists) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
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
		if a.GetOldObject().GetObjectKind().GroupVersionKind() != tenancyv1alpha1.SchemeGroupVersion.WithKind("Workspace") {
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

		cwt, err := o.resolveTypeRef(clusterName, cw.Spec.Type.ClusterWorkspaceTypeReference)
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

		if cw.Spec.Type.Cluster == "" {
			return admission.NewForbidden(a, fmt.Errorf("spec.type.cluster must be set"))
		} else if cw.Spec.Type.Cluster != tenancy.Cluster(logicalcluster.From(cwt).String()) {
			return admission.NewForbidden(a, fmt.Errorf("spec.type.cluster does not match spec.type.name and spec.type.path"))
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
				Resource:        "clusterworkspacetypes",
				Name:            alias.Name,
				ResourceRequest: true,
			}
			if decision, _, err := authz.Authorize(ctx, useAttr); err != nil {
				return admission.NewForbidden(a, fmt.Errorf("unable to determine access to cluster workspace type %s:%s: %w", logicalcluster.From(alias), alias.Name, err))
			} else if decision != authorizer.DecisionAllow {
				return admission.NewForbidden(a, fmt.Errorf("unable to use cluster workspace type %s:%s: missing verb='use' permission on clusterworkspacetype", logicalcluster.From(alias), alias.Name))
			}
		}

		// validate whether the workspace type is allowed in its parent, and the workspace type allows that parent
		this, err := o.thisWorkspaceLister.Get(client.ToClusterAwareKey(clusterName, tenancyv1alpha1.ThisWorkspaceName))
		if err != nil {
			return admission.NewForbidden(a, fmt.Errorf("workspace type canot be resolved: %w", err))
		}
		typeAnnotation, found := this.Annotations[tenancyv1beta1.WorkspaceTypeThisWorkspaceAnnotationKey]
		if !found {
			return admission.NewForbidden(a, fmt.Errorf("annotation %s on ThisWorkspace must be set", tenancyv1beta1.WorkspaceTypeThisWorkspaceAnnotationKey))
		}
		cwtCluster, cwtName := logicalcluster.New(typeAnnotation).Split()
		if cwtCluster.Empty() {
			return admission.NewForbidden(a, fmt.Errorf("annotation %s on ThisWorkspace must be in the form of cluster:name", tenancyv1beta1.WorkspaceTypeThisWorkspaceAnnotationKey))
		}
		parentCwt, err := o.typeLister.Get(client.ToClusterAwareKey(cwtCluster, cwtName))
		if err != nil {
			return admission.NewForbidden(a, fmt.Errorf("workspace type canot be resolved: %w", err))
		}
		parentAliases, err := o.transitiveTypeResolver.Resolve(parentCwt)
		if err != nil {
			return admission.NewForbidden(a, err)
		}

		thisTypePath := tenancy.TemporaryCanonicalPath(tenancy.Cluster(cwtCluster.String())).Join(cwtName)
		if err := validateAllowedParents(parentAliases, cwtAliases, thisTypePath.String(), cw.Spec.Type.String()); err != nil {
			return admission.NewForbidden(a, err)
		}
		if err := validateAllowedChildren(parentAliases, cwtAliases, thisTypePath.String(), cw.Spec.Type.String()); err != nil {
			return admission.NewForbidden(a, err)
		}
	}

	return nil
}

func (o *clusterWorkspaceTypeExists) ValidateInitialization() error {
	if o.typeLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an ClusterWorkspaceType lister")
	}
	if o.thisWorkspaceLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an ThisWorkspace lister")
	}
	return nil
}

func (o *clusterWorkspaceTypeExists) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	typesReady := informers.Tenancy().V1alpha1().ClusterWorkspaceTypes().Informer().HasSynced
	thisWorkspacesReady := informers.Tenancy().V1alpha1().ThisWorkspaces().Informer().HasSynced
	o.SetReadyFunc(func() bool {
		return typesReady() && thisWorkspacesReady()
	})
	o.typeLister = informers.Tenancy().V1alpha1().ClusterWorkspaceTypes().Lister()
	o.thisWorkspaceLister = informers.Tenancy().V1alpha1().ThisWorkspaces().Lister()
}

func (o *clusterWorkspaceTypeExists) SetDeepSARClient(client kcpkubernetesclientset.ClusterInterface) {
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
	cwt *tenancyv1alpha1.ClusterWorkspaceType,
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
	Resolve(t *tenancyv1alpha1.ClusterWorkspaceType) ([]*tenancyv1alpha1.ClusterWorkspaceType, error)
}

type transitiveTypeResolver struct {
	getter func(cluster logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspaceType, error)
}

func NewTransitiveTypeResolver(getter func(cluster logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspaceType, error)) TransitiveTypeResolver {
	return &transitiveTypeResolver{
		getter: getter,
	}
}

// Resolve returns all ClusterWorkspaceTypes that a given Type extends.
func (r *transitiveTypeResolver) Resolve(t *tenancyv1alpha1.ClusterWorkspaceType) ([]*tenancyv1alpha1.ClusterWorkspaceType, error) {
	ret, err := r.resolve(t, map[string]bool{}, map[string]bool{}, []string{})
	if err != nil {
		return nil, err
	}
	return append(ret, t), nil
}

func (r *transitiveTypeResolver) resolve(cwt *tenancyv1alpha1.ClusterWorkspaceType, seen, pathSeen map[string]bool, path []string) ([]*tenancyv1alpha1.ClusterWorkspaceType, error) {
	qualifiedName := logicalcluster.From(cwt).Join(cwt.Name).String()
	seen[qualifiedName] = true
	pathSeen[qualifiedName] = true
	defer func() { pathSeen[qualifiedName] = false }()
	path = append(path, qualifiedName)

	var ret []*tenancyv1alpha1.ClusterWorkspaceType
	for _, baseTypeRef := range cwt.Spec.Extend.With {
		qualifiedName := logicalcluster.New(baseTypeRef.Path).Join(tenancyv1alpha1.ObjectName(baseTypeRef.Name)).String()
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

		baseType, err := r.getter(logicalcluster.New(baseTypeRef.Path), tenancyv1alpha1.ObjectName(baseTypeRef.Name))
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

func validateAllowedParents(parentAliases, childAliases []*tenancyv1alpha1.ClusterWorkspaceType, parentType, childType string) error {
	var errs []error
	for _, childAlias := range childAliases {
		if childAlias.Spec.LimitAllowedParents == nil || len(childAlias.Spec.LimitAllowedParents.Types) == 0 {
			continue
		}

		qualifiedChild := logicalcluster.From(childAlias).Join(string(tenancyv1alpha1.TypeName(childAlias.Name))).String()

		if !allOfTheFormerExistInTheLater(parentAliases, childAlias.Spec.LimitAllowedParents.Types) {
			allowedSet := sets.NewString()
			for _, allowedParent := range childAlias.Spec.LimitAllowedParents.Types {
				allowedSet.Insert(logicalcluster.New(allowedParent.Path).Join(string(allowedParent.Name)).String())
			}

			implementedSet := sets.NewString()
			for _, parentAlias := range parentAliases {
				implementedSet.Insert(logicalcluster.From(parentAlias).Join(string(tenancyv1alpha1.TypeName(parentAlias.Name))).String())
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

func validateAllowedChildren(parentAliases, childAliases []*tenancyv1alpha1.ClusterWorkspaceType, parentType, childType string) error {
	var errs []error
	for _, parentAlias := range parentAliases {
		if parentAlias.Spec.LimitAllowedChildren == nil || len(parentAlias.Spec.LimitAllowedChildren.Types) == 0 {
			continue
		}
		if parentAlias.Spec.LimitAllowedChildren.None {
			return fmt.Errorf("workspace type %s cannot have any children", parentType)
		}

		qualifiedParent := logicalcluster.From(parentAlias).Join(string(tenancyv1alpha1.TypeName(parentAlias.Name))).String()

		if !allOfTheFormerExistInTheLater(childAliases, parentAlias.Spec.LimitAllowedChildren.Types) {
			allowedSet := sets.NewString()
			for _, allowedChild := range parentAlias.Spec.LimitAllowedChildren.Types {
				allowedSet.Insert(logicalcluster.New(allowedChild.Path).Join(string(allowedChild.Name)).String())
			}

			implementedSet := sets.NewString()
			for _, childAlias := range childAliases {
				implementedSet.Insert(logicalcluster.From(childAlias).Join(string(tenancyv1alpha1.TypeName(childAlias.Name))).String())
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

func allOfTheFormerExistInTheLater(objectAliases []*tenancyv1alpha1.ClusterWorkspaceType, allowedTypes []tenancyv1alpha1.ClusterWorkspaceTypeReference) bool {
	allowedAliasSet := sets.NewString()
	for _, allowed := range allowedTypes {
		qualified := logicalcluster.New(allowed.Path).Join(tenancyv1alpha1.ObjectName(allowed.Name)).String()
		allowedAliasSet.Insert(qualified)
	}

	for _, obj := range objectAliases {
		qualifiedObj := logicalcluster.From(obj).Join(obj.Name).String()
		if allowedAliasSet.Has(qualifiedObj) {
			return true
		}
	}

	return false
}
