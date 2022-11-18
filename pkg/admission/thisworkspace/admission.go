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

package thisworkspace

import (
	"context"
	"fmt"
	"io"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

// Protects deletion of ThisWorkspace if spec.directlyDeletable is false.

const (
	PluginName = "tenancy.kcp.dev/ThisWorkspace"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &thisWorkspace{
				Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete),
			}, nil
		})
}

type thisWorkspace struct {
	*admission.Handler

	thisWorkspaceLister tenancyv1alpha1listers.ThisWorkspaceClusterLister
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&thisWorkspace{})
var _ = admission.MutationInterface(&thisWorkspace{})
var _ = admission.InitializationValidator(&thisWorkspace{})
var _ = kcpinitializers.WantsKcpInformers(&thisWorkspace{})

var phaseOrdinal = map[tenancyv1alpha1.WorkspacePhaseType]int{
	tenancyv1alpha1.WorkspacePhaseType(""):     1,
	tenancyv1alpha1.WorkspacePhaseScheduling:   2,
	tenancyv1alpha1.WorkspacePhaseInitializing: 3,
	tenancyv1alpha1.WorkspacePhaseReady:        4,
}

// Admit adds type initializer to status on transition to initializing phase.
func (o *thisWorkspace) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("thisworkspaces") {
		return nil
	}

	switch a.GetOperation() {
	case admission.Update:
		if a.GetObject().GetObjectKind().GroupVersionKind() != tenancyv1alpha1.SchemeGroupVersion.WithKind("ThisWorkspace") {
			return nil
		}
		u, ok := a.GetObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetObject())
		}
		this := &tenancyv1alpha1.ThisWorkspace{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, this); err != nil {
			return fmt.Errorf("failed to convert unstructured to ThisWorkspace: %w", err)
		}

		if a.GetOldObject().GetObjectKind().GroupVersionKind() != tenancyv1alpha1.SchemeGroupVersion.WithKind("ThisWorkspace") {
			return nil
		}
		oldU, ok := a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetOldObject())
		}
		old := &tenancyv1alpha1.ThisWorkspace{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(oldU.Object, old); err != nil {
			return fmt.Errorf("failed to convert unstructured to ThisWorkspace: %w", err)
		}

		// we only admit at state transition to initializing
		transitioningToInitializing := old.Status.Phase != tenancyv1alpha1.WorkspacePhaseInitializing && this.Status.Phase == tenancyv1alpha1.WorkspacePhaseInitializing
		if !transitioningToInitializing {
			return nil
		}

		this.Status.Initializers = this.Spec.Initializers

		return updateUnstructured(u, this)
	}

	return nil
}

func (o *thisWorkspace) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("thisworkspaces") {
		return nil
	}

	groups := sets.NewString(a.GetUserInfo().GetGroups()...)
	if groups.Has("system:masters") || groups.Has(bootstrap.SystemLogicalClusterAdmin) || groups.Has(bootstrap.SystemKcpWorkspaceBootstrapper) {
		return nil
	}

	switch a.GetOperation() {
	case admission.Update:
		u, ok := a.GetObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetObject())
		}
		this := &tenancyv1alpha1.ThisWorkspace{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, this); err != nil {
			return fmt.Errorf("failed to convert unstructured to ThisWorkspace: %w", err)
		}

		u, ok = a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetObject())
		}
		old := &tenancyv1alpha1.ThisWorkspace{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, old); err != nil {
			return fmt.Errorf("failed to convert unstructured to ThisWorkspace: %w", err)
		}

		oldSpec := toSet(old.Spec.Initializers)
		newSpec := toSet(this.Spec.Initializers)
		oldStatus := toSet(old.Status.Initializers)
		newStatus := toSet(this.Status.Initializers)

		if !oldSpec.Equal(newSpec) {
			return admission.NewForbidden(a, fmt.Errorf("spec.initializers is immutable"))
		}

		transitioningToInitializing := old.Status.Phase != tenancyv1alpha1.WorkspacePhaseInitializing && this.Status.Phase == tenancyv1alpha1.WorkspacePhaseInitializing
		if transitioningToInitializing && !newSpec.Equal(newStatus) {
			return admission.NewForbidden(a, fmt.Errorf("status.initializers do not equal spec.initializers"))
		}

		if !transitioningToInitializing && this.Status.Phase == tenancyv1alpha1.WorkspacePhaseInitializing && !oldStatus.IsSuperset(newStatus) {
			return admission.NewForbidden(a, fmt.Errorf("status.initializers must not grow"))
		}

		if this.Status.Phase != tenancyv1alpha1.WorkspacePhaseInitializing && !oldStatus.Equal(newStatus) {
			return admission.NewForbidden(a, fmt.Errorf("status.initializers is immutable after initilization"))
		}

		if old.Status.Phase == tenancyv1alpha1.WorkspacePhaseInitializing && this.Status.Phase != tenancyv1alpha1.WorkspacePhaseInitializing {
			if len(this.Status.Initializers) > 0 {
				return admission.NewForbidden(a, fmt.Errorf("status.initializers is not empty"))
			}
		}

		if phaseOrdinal[old.Status.Phase] > phaseOrdinal[this.Status.Phase] {
			return admission.NewForbidden(a, fmt.Errorf("cannot transition from %q to %q", old.Status.Phase, this.Status.Phase))
		}

		return nil

	case admission.Delete:
		this, err := o.thisWorkspaceLister.Cluster(clusterName).Get(tenancyv1alpha1.ThisWorkspaceName)
		if err != nil {
			return fmt.Errorf("ThisWorkspace cannot be deleted")
		}
		groups := sets.NewString(a.GetUserInfo().GetGroups()...)
		if !this.Spec.DirectlyDeletable && !groups.Has("system:masters") && !groups.Has(bootstrap.SystemLogicalClusterAdmin) {
			return admission.NewForbidden(a, fmt.Errorf("ThisWorkspace cannot be deleted"))
		}

	case admission.Create:
		return admission.NewForbidden(a, fmt.Errorf("ThisWorkspace cannot be created"))
	}

	return nil
}

func (o *thisWorkspace) ValidateInitialization() error {
	if o.thisWorkspaceLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an ThisWorkspace lister")
	}
	return nil
}

func (o *thisWorkspace) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	thisWorkspacesReady := informers.Tenancy().V1alpha1().ThisWorkspaces().Informer().HasSynced
	o.SetReadyFunc(func() bool {
		return thisWorkspacesReady()
	})
	o.thisWorkspaceLister = informers.Tenancy().V1alpha1().ThisWorkspaces().Lister()
}

func toSet(initializers []tenancyv1alpha1.WorkspaceInitializer) sets.String {
	ret := sets.NewString()
	for _, initializer := range initializers {
		ret.Insert(string(initializer))
	}
	return ret
}

// updateUnstructured updates the given unstructured object to match the given cluster workspace.
func updateUnstructured(u *unstructured.Unstructured, cw *tenancyv1alpha1.ThisWorkspace) error {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cw)
	if err != nil {
		return err
	}
	u.Object = raw
	return nil
}
