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

package apiexport

import (
	"context"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"

	"github.com/kcp-dev/kcp/pkg/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	builtinapiexport "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
)

// PluginName is the name used to identify this admission webhook.
const PluginName = "apis.kcp.io/APIExport"

var mutationVerbs = []string{"create", "update", "patch", "delete", "deletecollection"}

// Register registers the reserved name admission webhook.
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return NewAPIExportAdmission(builtinapiexport.IsBuiltInAPI), nil
		})
}

// APIExportAdmission is an admission plugin for checking APIExport validity.
type APIExportAdmission struct {
	*admission.Handler

	isBuiltIn func(apisv1alpha1.GroupResource) bool
}

// NewAPIExportAdmission constructs a new APIExportAdmission admission plugin.
func NewAPIExportAdmission(isBuiltIn func(apisv1alpha1.GroupResource) bool) *APIExportAdmission {
	return &APIExportAdmission{
		Handler:   admission.NewHandler(admission.Create, admission.Update),
		isBuiltIn: isBuiltIn,
	}
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&APIExportAdmission{})

// Validate ensures that the APIExport is valid.
func (e *APIExportAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != apisv1alpha1.Resource("apiexports") {
		return nil
	}

	if a.GetKind().GroupKind() != apisv1alpha1.Kind("APIExport") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	ae := &apisv1alpha1.APIExport{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, ae); err != nil {
		return fmt.Errorf("failed to convert unstructured to APIExport: %w", err)
	}

	errs := field.ErrorList{}
	for i, pc := range ae.Spec.PermissionClaims {
		restrictToVerbs := sets.NewString(pc.Verbs.RestrictTo...)
		claimedVerbs := sets.NewString(pc.Verbs.Claimed...)
		commonVerbs := claimedVerbs.Intersection(restrictToVerbs)
		claimsIsWildcard := len(pc.Verbs.Claimed) > 0 && pc.Verbs.Claimed[0] == "*"
		restrictToIsWildcard := len(pc.Verbs.RestrictTo) > 0 && pc.Verbs.RestrictTo[0] == "*"

		hasMutationConflict := commonVerbs.HasAny(mutationVerbs...)
		hasMutationConflict = hasMutationConflict || claimsIsWildcard && restrictToVerbs.HasAny(mutationVerbs...)
		hasMutationConflict = hasMutationConflict || restrictToIsWildcard && claimedVerbs.HasAny(mutationVerbs...)
		hasMutationConflict = hasMutationConflict || restrictToIsWildcard && claimsIsWildcard

		if hasMutationConflict {
			errs = append(errs, field.Invalid(
				field.NewPath("spec").
					Child("permissionClaims").
					Index(i).
					Child("verbs"),
				"",
				"verbs.claimed and verbs.restrictTo must not have intersecting mutation verbs"))
		}

		if pc.IdentityHash == "" && !e.isBuiltIn(pc.GroupResource) && pc.Group != apis.GroupName {
			errs = append(errs, field.Invalid(
				field.NewPath("spec").
					Child("permissionClaims").
					Index(i).
					Child("identityHash"),
				"",
				"identityHash is required for API types that are not built-in"))
		}
	}

	if len(errs) > 0 {
		return admission.NewForbidden(a, fmt.Errorf("%v", errs))
	}

	return nil
}
