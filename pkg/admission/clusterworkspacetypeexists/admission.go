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

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clusters"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
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

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	cwt, err := o.typeLister.Get(clusters.ToClusterAwareKey(clusterName, strings.ToLower(cw.Spec.Type)))
	if err != nil && apierrors.IsNotFound(err) {
		if cw.Spec.Type == "Universal" {
			return nil // Universal is always valid
		}
		return admission.NewForbidden(a, fmt.Errorf("spec.type %q does not exist", cw.Spec.Type))
	} else if err != nil {
		return admission.NewForbidden(a, err)
	}

	if a.GetOperation() == admission.Create {
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

	// add initializers from type to workspace
	existing := sets.NewString()
	for _, i := range cw.Status.Initializers {
		existing.Insert(string(i))
	}
	for _, i := range cwt.Spec.Initializers {
		if !existing.Has(string(i)) {
			cw.Status.Initializers = append(cw.Status.Initializers, i)
		}
	}

	return updateUnstructured(u, cw)
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

		cwt, err = o.typeLister.Get(clusters.ToClusterAwareKey(clusterName, strings.ToLower(cw.Spec.Type)))
		if err != nil && apierrors.IsNotFound(err) {
			if cw.Spec.Type == "Universal" {
				return nil // Universal is always valid
			}
			return admission.NewForbidden(a, fmt.Errorf("spec.type %q does not exist", cw.Spec.Type))
		} else if err != nil {
			return admission.NewForbidden(a, err)
		}
	}

	// add initializers from type to workspace
	if a.GetOperation() == admission.Update && transitioningToInitializing {
		// this is a transition to initializing. Check that all initializers are there
		// (no other admission plugin removed any).
		existing := sets.NewString()
		for _, initializer := range cw.Status.Initializers {
			existing.Insert(string(initializer))
		}
		for _, initializer := range cwt.Spec.Initializers {
			if !existing.Has(string(initializer)) {
				return admission.NewForbidden(a, fmt.Errorf("spec.initializers %q does not exist", initializer))
			}
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
	return nil
}

func (o *clusterWorkspaceTypeExists) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	o.SetReadyFunc(informers.Tenancy().V1alpha1().ClusterWorkspaceTypes().Informer().HasSynced)
	o.typeLister = informers.Tenancy().V1alpha1().ClusterWorkspaceTypes().Lister()
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
