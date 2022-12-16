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

package reservednames

import (
	"context"
	"io"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

// PluginName is the name used to identify this admission webhook.
const PluginName = "apis.kcp.dev/ReservedNames"

// Register registers the reserved name admission webhook.
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return NewReservedNames(), nil
		})
}

// A reservedNameFn determines whether the name of the object is reserved.
type reservedNameFn func(resource schema.GroupResource, kind schema.GroupKind, name string) bool

// newReservedNameFunc builds a reserved name check for the given resource and
// kind.
func newReservedNameFn(resource schema.GroupResource, kind schema.GroupKind, reserved ...string) reservedNameFn {
	res := make(map[string]struct{}, len(reserved))
	for _, r := range reserved {
		res[r] = struct{}{}
	}
	check := func(name string) bool {
		_, reserved := res[name]
		return reserved
	}
	return func(objResource schema.GroupResource, objKind schema.GroupKind, objName string) bool {
		if objResource != resource {
			return false
		}
		if objKind != kind {
			return false
		}
		return check(objName)
	}
}

// ReservedNames is an admission plugin for checking reserved object names.
type ReservedNames struct {
	*admission.Handler

	reservedNameFns []reservedNameFn
}

// NewReservedNames constructs a new ReservedNames admission plugin.
func NewReservedNames() *ReservedNames {
	return &ReservedNames{
		Handler: admission.NewHandler(admission.Create),
		reservedNameFns: []reservedNameFn{
			newReservedNameFn(
				tenancyv1alpha1.Resource("clusterworkspaces"),
				tenancyv1alpha1.Kind("ClusterWorkspace"),
				tenancyv1alpha1.ClusterWorkspaceReservedNames()...,
			),
			newReservedNameFn(
				tenancyv1alpha1.Resource("workspacetypes"),
				tenancyv1alpha1.Kind("WorkspaceType"),
				tenancyv1alpha1.WorkspaceTypesReservedNames()...,
			),
		},
	}
}

// Ensure that the required admission interfaces are implemented.
// NOTE(hasheddan): we must use mutation rather than validation because OpenAPI
// validation will occur prior to validation webhook.
var _ = admission.MutationInterface(&ReservedNames{})

// Admit ensures that the object does not violate reserved name constraints.
func (o *ReservedNames) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	resource, kind := a.GetResource().GroupResource(), a.GetKind().GroupKind()
	for _, isReserved := range o.reservedNameFns {
		if isReserved(resource, kind, a.GetName()) {
			return admission.NewForbidden(a, field.Invalid(field.NewPath("metadata").Child("name"), a.GetName(), "name is reserved"))
		}
	}

	return nil
}
