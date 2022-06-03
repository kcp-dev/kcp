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

package clusterworkspace

import (
	"context"
	"errors"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

// Validate ClusterWorkspace creation and updates for
// - immutability of fields like type
// - valid phase transitions fulfilling pre-conditions
// - status.location.current and status.baseURL cannot be unset.

// Mutate ClusterWorkspace creation and updates for
// - initializers are short enough to be put into a label
// - consistency of phase and initializers with labels

const (
	PluginName = "tenancy.kcp.dev/ClusterWorkspace"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &clusterWorkspace{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

type clusterWorkspace struct {
	*admission.Handler
}

// Ensure that the required admission interfaces are implemented.
var _ admission.ValidationInterface = &clusterWorkspace{}

var phaseOrdinal = map[tenancyv1alpha1.ClusterWorkspacePhaseType]int{
	tenancyv1alpha1.ClusterWorkspacePhaseType(""):     1,
	tenancyv1alpha1.ClusterWorkspacePhaseScheduling:   2,
	tenancyv1alpha1.ClusterWorkspacePhaseInitializing: 3,
	tenancyv1alpha1.ClusterWorkspacePhaseReady:        4,
}

// Validate ensures that
// - the workspace only does a valid phase transition
// - has a valid type
// - has valid initializers when transitioning to initializing
func (o *clusterWorkspace) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("clusterworkspaces") {
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

	if a.GetOperation() == admission.Update {
		u, ok = a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetOldObject())
		}
		old := &tenancyv1alpha1.ClusterWorkspace{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, old); err != nil {
			return fmt.Errorf("failed to convert unstructured to ClusterWorkspace: %w", err)
		}

		if errs := validation.ValidateImmutableField(cw.Spec.Type, old.Spec.Type, field.NewPath("spec", "type")); len(errs) > 0 {
			return admission.NewForbidden(a, errs.ToAggregate())
		}
		if old.Spec.Type != cw.Spec.Type {
			return admission.NewForbidden(a, errors.New("spec.type is immutable"))
		}

		if old.Status.Location.Current != "" && cw.Status.Location.Current == "" {
			return admission.NewForbidden(a, errors.New("status.location.current cannot be unset"))
		}

		if old.Status.BaseURL != "" && cw.Status.BaseURL == "" {
			return admission.NewForbidden(a, errors.New("status.baseURL cannot be unset"))
		}

		if phaseOrdinal[old.Status.Phase] > phaseOrdinal[cw.Status.Phase] {
			return admission.NewForbidden(a, fmt.Errorf("cannot transition from %q to %q", old.Status.Phase, cw.Status.Phase))
		}
	}

	if phaseOrdinal[cw.Status.Phase] > phaseOrdinal[tenancyv1alpha1.ClusterWorkspacePhaseInitializing] && len(cw.Status.Initializers) > 0 {
		return admission.NewForbidden(a, fmt.Errorf("spec.initializers must be empty for phase %s", cw.Status.Phase))
	}

	if phaseOrdinal[cw.Status.Phase] > phaseOrdinal[tenancyv1alpha1.ClusterWorkspacePhaseScheduling] {
		if cw.Status.Location.Current == "" {
			return admission.NewForbidden(a, fmt.Errorf("status.location.current must be set for phase %s", cw.Status.Phase))
		}
		if cw.Status.BaseURL == "" {
			return admission.NewForbidden(a, fmt.Errorf("status.baseURL must be set for phase %s", cw.Status.Phase))
		}
	}

	return nil
}
