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
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/kcp-dev/logicalcluster"

	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clusters"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	tenancyv1alpha1lister "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

const (
	PluginName = "tenancy.kcp.dev/ClusterWorkspaceTypeExists"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &clusterWorkspaceTypeExists{
				Handler:          admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: delegated.NewDelegatedAuthorizer,
			}, nil
		})
}

// clusterWorkspaceTypeExists  does the following
// - it checks existence of ClusterWorkspaceType in the same workspace,
// - it applies the ClusterWorkspaceType initializers to the ClusterWorkspace when it
//   transitions to the Initializing state.
type clusterWorkspaceTypeExists struct {
	*admission.Handler
	typeLister        tenancyv1alpha1lister.ClusterWorkspaceTypeLister
	workspaceLister   tenancyv1alpha1lister.ClusterWorkspaceLister
	kubeClusterClient *kubernetes.Cluster

	createAuthorizer delegated.DelegatedAuthorizerFactory
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.MutationInterface(&clusterWorkspaceTypeExists{})
var _ = admission.ValidationInterface(&clusterWorkspaceTypeExists{})
var _ = admission.InitializationValidator(&clusterWorkspaceTypeExists{})
var _ = kcpinitializers.WantsKcpInformers(&clusterWorkspaceTypeExists{})
var _ = kcpinitializers.WantsKubeClusterClient(&clusterWorkspaceTypeExists{})

// Admit adds type initializer on transition to initializing phase.
func (o *clusterWorkspaceTypeExists) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("clusterworkspaces") {
		return nil
	}

	if a.GetObject().GetObjectKind().GroupVersionKind() != tenancyv1alpha1.Kind("ClusterWorkspace").WithVersion("v1alpha1") {
		return nil
	}
	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	cw := &tenancyv1alpha1.ClusterWorkspace{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cw); err != nil {
		return fmt.Errorf("failed to convert unstructured to ClusterWorkspace: %w", err)
	}

	if !o.WaitForReady() {
		return admission.NewForbidden(a, fmt.Errorf("not yet ready to handle request"))
	}

	if a.GetOperation() == admission.Create {
		// ensure the user's UID is recorded in annotations
		user := a.GetUserInfo()
		info := &authenticationv1.UserInfo{
			Username: user.GetName(),
			UID:      user.GetUID(),
			Groups:   user.GetGroups(),
		}
		extra := map[string]authenticationv1.ExtraValue{}
		for k, v := range user.GetExtra() {
			extra[k] = v
		}
		info.Extra = extra
		rawInfo, err := json.Marshal(info)
		if err != nil {
			return fmt.Errorf("failed to marshal user info: %w", err)
		}

		if cw.Annotations == nil {
			cw.Annotations = map[string]string{}
		}
		cw.Annotations[tenancyv1alpha1.ClusterWorkspaceOwnerAnnotationKey] = string(rawInfo)

		// if the user has not provided any type, use the default from the parent workspace
		empty := tenancyv1alpha1.ClusterWorkspaceTypeReference{}
		if cw.Spec.Type == empty {
			clusterName, err := genericapirequest.ClusterNameFrom(ctx)
			if err != nil {
				return apierrors.NewInternalError(err)
			}
			parentCwt, resolutionError := o.resolveParentType(clusterName)
			if resolutionError != nil {
				return admission.NewForbidden(a, resolutionError)
			}
			if parentCwt == nil || parentCwt.Spec.DefaultChildWorkspaceType == nil {
				return admission.NewForbidden(a, errors.New("spec.type must be set"))
			}
			cw.Spec.Type = *parentCwt.Spec.DefaultChildWorkspaceType
		}
		cwt, resolutionError := o.resolveType(cw.Spec.Type)
		if resolutionError != nil {
			return admission.NewForbidden(a, resolutionError)
		}
		addAdditionalWorkspaceLabels(cwt, cw)

		return updateUnstructured(u, cw)
	}

	if a.GetOperation() != admission.Update {
		return nil
	}

	if a.GetOldObject().GetObjectKind().GroupVersionKind() != tenancyv1alpha1.Kind("ClusterWorkspace").WithVersion("v1alpha1") {
		return nil
	}
	oldU, ok := a.GetOldObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetOldObject())
	}
	old := &tenancyv1alpha1.ClusterWorkspace{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(oldU.Object, old); err != nil {
		return fmt.Errorf("failed to convert unstructured to ClusterWorkspace: %w", err)
	}

	// we only admit at state transition to initializing
	transitioningToInitializing :=
		old.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseInitializing &&
			cw.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseInitializing
	if !transitioningToInitializing {
		return nil
	}

	cwt, resolutionError := o.resolveType(cw.Spec.Type)
	if resolutionError != nil {
		return admission.NewForbidden(a, resolutionError)
	}

	// add initializers from type to workspace
	for _, initializer := range cwt.Status.Initializers {
		cw.Status.Initializers = initialization.EnsureInitializerPresent(initializer, cw.Status.Initializers)
	}

	return updateUnstructured(u, cw)
}

func (o *clusterWorkspaceTypeExists) resolveValidType(parentClusterName logicalcluster.Name, workspaceName string, ref tenancyv1alpha1.ClusterWorkspaceTypeReference) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
	cwt, err := o.resolveType(ref)
	if err != nil {
		return nil, err
	}
	parentCwt, err := o.resolveParentType(parentClusterName)
	if err != nil {
		return nil, err
	}
	parentRef := tenancyv1alpha1.ReferenceFor(parentCwt)
	if !parentCwt.Spec.AllowAnyChildWorkspaceTypes && !setContainsAnyType(parentCwt.Spec.AllowedChildWorkspaceTypes, cwt.Status.TypeAliases) {
		return nil, fmt.Errorf("parent cluster workspace %q (of type %s) does not allow for child workspaces of type %s", parentClusterName, parentRef.String(), ref.String())
	}
	if !cwt.Spec.AllowAnyParentWorkspaceTypes && !setContainsAnyType(cwt.Spec.AllowedParentWorkspaceTypes, parentCwt.Status.TypeAliases) {
		return nil, fmt.Errorf("cluster workspace %q (of type %s) does not allow for parent workspaces of type %s", workspaceName, ref.String(), parentRef.String())
	}
	return cwt, nil
}

func setContainsAnyType(set, queries []tenancyv1alpha1.ClusterWorkspaceTypeReference) bool {
	for _, allowed := range set {
		for _, query := range queries {
			if allowed.Equal(query) {
				return true
			}
		}
	}
	return false
}

func (o *clusterWorkspaceTypeExists) resolveParentType(parentClusterName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
	grandparent, parentHasParent := parentClusterName.Parent()
	if !parentHasParent {
		// the clusterWorkspace exists in the root logical cluster, and therefore there is no
		// higher clusterWorkspaceType to check for allowed sub-types; the mere presence of the
		// clusterWorkspaceType is enough. We return a fake object here to express this behavior
		return tenancyv1alpha1.RootWorkspaceType, nil
	}
	parentCluster, err := o.workspaceLister.Get(clusters.ToClusterAwareKey(grandparent, parentClusterName.Base()))
	if err != nil {
		return nil, fmt.Errorf("could not resolve parent cluster workspace %q: %w", parentClusterName.String(), err)
	}
	parentCwt, resolutionError := o.resolveType(parentCluster.Spec.Type)
	if resolutionError != nil {
		return nil, fmt.Errorf("could not resolve type %s of parent cluster workspace %q: %w", parentCluster.Spec.Type.String(), parentClusterName.String(), resolutionError)
	}
	return parentCwt, nil
}

func (o *clusterWorkspaceTypeExists) resolveType(ref tenancyv1alpha1.ClusterWorkspaceTypeReference) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
	cwt, err := o.typeLister.Get(clusters.ToClusterAwareKey(logicalcluster.New(ref.Path), tenancyv1alpha1.ObjectName(ref.Name)))
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("spec.type %s does not exist", ref.String())
	}
	if err != nil {
		return nil, err
	}
	if !conditions.IsTrue(cwt, conditionsv1alpha1.ReadyCondition) {
		return nil, fmt.Errorf("ClusterWorkspaceType %s is not ready", tenancyv1alpha1.ReferenceFor(cwt))
	}
	return cwt, nil
}

// Validate ensures that
// - has a valid type
// - has valid initializers when transitioning to initializing
func (o *clusterWorkspaceTypeExists) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("clusterworkspaces") {
		return nil
	}

	if a.GetObject().GetObjectKind().GroupVersionKind() != tenancyv1alpha1.Kind("ClusterWorkspace").WithVersion("v1alpha1") {
		return nil
	}
	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	cw := &tenancyv1alpha1.ClusterWorkspace{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cw); err != nil {
		return fmt.Errorf("failed to convert unstructured to ClusterWorkspace: %w", err)
	}

	// first all steps where we need no lister
	var old *tenancyv1alpha1.ClusterWorkspace
	var transitioningToInitializing bool
	switch a.GetOperation() {
	case admission.Update:
		if a.GetOldObject().GetObjectKind().GroupVersionKind() != tenancyv1alpha1.Kind("ClusterWorkspace").WithVersion("v1alpha1") {
			return nil
		}
		u, ok = a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetOldObject())
		}
		old = &tenancyv1alpha1.ClusterWorkspace{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, old); err != nil {
			return fmt.Errorf("failed to convert unstructured to ClusterWorkspace: %w", err)
		}

		transitioningToInitializing = old.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseInitializing &&
			cw.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseInitializing
	}

	if !o.WaitForReady() {
		return admission.NewForbidden(a, fmt.Errorf("not yet ready to handle request"))
	}

	if a.GetOperation() == admission.Update {
		if old.Spec.Type != cw.Spec.Type {
			return admission.NewForbidden(a, errors.New("spec.type is immutable"))
		}
	}

	// check type on create and on state transition
	// TODO(sttts): there is a race that the type can be deleted between scheduling and initializing
	//              but we cannot add initializers in status on create. A controller doing that wouldn't fix
	//		        the race either. So, ¯\_(ツ)_/¯. Chance is low. Object can be deleted, or a condition could should
	//              show it failing.
	var cwt *tenancyv1alpha1.ClusterWorkspaceType
	if (a.GetOperation() == admission.Update && transitioningToInitializing) || a.GetOperation() == admission.Create {
		clusterName, err := genericapirequest.ClusterNameFrom(ctx)
		if err != nil {
			return apierrors.NewInternalError(err)
		}

		var resolutionError error
		cwt, resolutionError = o.resolveValidType(clusterName, cw.Name, cw.Spec.Type)
		if resolutionError != nil {
			return admission.NewForbidden(a, resolutionError)
		}
	}

	// add initializer from type to workspace
	if a.GetOperation() == admission.Update && transitioningToInitializing && cwt.Spec.Initializer {
		// this is a transition to initializing. Check that all initializers are there
		// (no other admission plugin removed any).
		if initializer := initialization.InitializerForType(cwt); !initialization.InitializerPresent(initializer, cw.Status.Initializers) {
			return admission.NewForbidden(a, fmt.Errorf("spec.initializers %q does not exist", initializer))
		}
	}

	// verify that the type can be used by the given user
	if a.GetOperation() == admission.Create {
		authz, err := o.createAuthorizer(logicalcluster.From(cwt), o.kubeClusterClient)
		if err != nil {
			return admission.NewForbidden(a, fmt.Errorf("unable to determine access to cluster workspace type %q", cw.Spec.Type))
		}

		useAttr := authorizer.AttributesRecord{
			User:            a.GetUserInfo(),
			Verb:            "use",
			APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
			APIVersion:      tenancyv1alpha1.SchemeGroupVersion.Version,
			Resource:        "clusterworkspacetypes",
			Name:            cwt.Name,
			ResourceRequest: true,
		}
		if decision, _, err := authz.Authorize(ctx, useAttr); err != nil {
			return admission.NewForbidden(a, fmt.Errorf("unable to determine access to cluster workspace type: %w", err))
		} else if decision != authorizer.DecisionAllow {
			return admission.NewForbidden(a, fmt.Errorf("unable to use cluster workspace type %q: missing verb='use' permission on clusterworkspacetype", cw.Spec.Type))
		}
	}

	return nil
}

func (o *clusterWorkspaceTypeExists) ValidateInitialization() error {
	if o.typeLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an ClusterWorkspaceType lister")
	}
	if o.workspaceLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an ClusterWorkspace lister")
	}
	return nil
}

func (o *clusterWorkspaceTypeExists) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	typesReady := informers.Tenancy().V1alpha1().ClusterWorkspaceTypes().Informer().HasSynced
	workspacesReady := informers.Tenancy().V1alpha1().ClusterWorkspaces().Informer().HasSynced
	o.SetReadyFunc(func() bool {
		return typesReady() && workspacesReady()
	})
	o.typeLister = informers.Tenancy().V1alpha1().ClusterWorkspaceTypes().Lister()
	o.workspaceLister = informers.Tenancy().V1alpha1().ClusterWorkspaces().Lister()
}

func (o *clusterWorkspaceTypeExists) SetKubeClusterClient(kubeClusterClient *kubernetes.Cluster) {
	o.kubeClusterClient = kubeClusterClient
}

// updateUnstructured updates the given unstructured object to match the given cluster workspace.
func updateUnstructured(u *unstructured.Unstructured, cw *tenancyv1alpha1.ClusterWorkspace) error {
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
	cw *tenancyv1alpha1.ClusterWorkspace,
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
