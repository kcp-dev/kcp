/*
Copyright 2022 The kcp Authors.

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
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"

	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	builtinapiexport "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
)

// PluginName is the name used to identify this admission webhook.
const PluginName = "apis.kcp.io/APIExport"

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

	isBuiltIn func(apis.GroupResource) bool
}

// NewAPIExportAdmission constructs a new APIExportAdmission admission plugin.
func NewAPIExportAdmission(isBuiltIn func(apis.GroupResource) bool) *APIExportAdmission {
	return &APIExportAdmission{
		Handler:   admission.NewHandler(admission.Create, admission.Update),
		isBuiltIn: isBuiltIn,
	}
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&APIExportAdmission{})

// Validate ensures that the APIExport is valid.
func (e *APIExportAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != apisv1alpha2.Resource("apiexports") || a.GetKind().GroupKind() != apisv1alpha2.Kind("APIExport") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	switch a.GetKind().GroupVersion().Version {
	case apisv1alpha1.SchemeGroupVersion.Version:
		// v1alpha1 is deprecated, but we still need to support it for a while
		// for backward compatibility.
		// We have one non-shared validation, which checks if annotations, carrying overhanging
		// resource schemas, are set correctly.

		ae := &apisv1alpha1.APIExport{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, ae); err != nil {
			return fmt.Errorf("failed to convert unstructured to APIExport: %w", err)
		}

		// Before we convert to v1alpha2, we need to validate the annotations overhanging:
		if err := validateOverhangingResourceSchemas(ctx, a, ae); err != nil {
			return admission.NewForbidden(a, err)
		}
		if err := validateOverhangingPermissionClaims(ctx, a, ae); err != nil {
			return admission.NewForbidden(a, err)
		}

		v2 := new(apisv1alpha2.APIExport)
		err := apisv1alpha2.Convert_v1alpha1_APIExport_To_v1alpha2_APIExport(ae, v2, nil)
		if err != nil {
			return fmt.Errorf("failed to convert v1alpha1 APIExport to v1alpha2: %w", err)
		}

		if err := e.validatev1alpha2(ctx, a, v2); err != nil {
			return err
		}
	case apisv1alpha2.SchemeGroupVersion.Version:
		// v1alpha2 is the current version.
		ae := &apisv1alpha2.APIExport{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, ae); err != nil {
			return fmt.Errorf("failed to convert unstructured to APIExport: %w", err)
		}
		if err := e.validatev1alpha2(ctx, a, ae); err != nil {
			return err
		}

	default:
		return admission.NewForbidden(a,
			field.Invalid(
				field.NewPath("apiVersion"),
				a.GetKind().GroupVersion().String(),
				fmt.Sprintf("unsupported API version %s", a.GetKind().GroupVersion().String())))
	}
	return nil
}

func (e *APIExportAdmission) validatev1alpha2(_ context.Context, a admission.Attributes, ae *apisv1alpha2.APIExport) (err error) {
	for i, pc := range ae.Spec.PermissionClaims {
		if pc.IdentityHash == "" && !e.isBuiltIn(pc.GroupResource) && pc.Group != apis.GroupName {
			return admission.NewForbidden(a,
				field.Invalid(
					field.NewPath("spec").
						Child("permissionClaims").
						Index(i).
						Child("identityHash"),
					"",
					"identityHash is required for API types that are not built-in"))
		}
	}

	for i, rs := range ae.Spec.Resources {
		if err := validateResourceSchema(rs, field.NewPath("spec").Child("resources").Index(i)); err != nil {
			return admission.NewForbidden(a, err)
		}
	}

	return nil
}

func validateResourceSchema(resourceSchema apisv1alpha2.ResourceSchema, path *field.Path) *field.Error {
	group := resourceSchema.Group
	if group == "" {
		group = "core"
	}

	// TODO(mjudeikis): Once v1alpha1 is removed, we can relax this if we chose to.
	// We should revisit this once we have a better understanding of the APIExport usage patterns.
	expectedSuffix := fmt.Sprintf(".%s.%s", resourceSchema.Name, group)
	if !strings.HasSuffix(resourceSchema.Schema, expectedSuffix) {
		return field.Invalid(path.Child("schema"), resourceSchema.Schema, fmt.Sprintf("must end in %s", expectedSuffix))
	}

	return nil
}

func validateOverhangingResourceSchemas(_ context.Context, _ admission.Attributes, ae *apisv1alpha1.APIExport) error {
	// TODO(mjudeikis): Remove this once we are sure that all APIExport objects are
	// converted to v1alpha2.
	if _, ok := ae.Annotations[apisv1alpha2.ResourceSchemasAnnotation]; ok {
		// validate if we can decode overhanging resource schemas. If not, we will fail.
		var overhanging []apisv1alpha2.ResourceSchema
		if err := json.Unmarshal([]byte(ae.Annotations[apisv1alpha2.ResourceSchemasAnnotation]), &overhanging); err != nil {
			return field.Invalid(field.NewPath("metadata").Child("annotations").Key(apisv1alpha2.ResourceSchemasAnnotation), ae.Annotations[apisv1alpha2.ResourceSchemasAnnotation], "failed to decode overhanging resource schemas")
		}

		// validate duplicates. We could have duplicates in annotations itself, or in spec + annotations.
		// We convert to v2 to check for duplicates.
		v2Schemas := make([]apisv1alpha2.ResourceSchema, len(ae.Spec.LatestResourceSchemas))
		err := apisv1alpha2.Convert_v1alpha1_LatestResourceSchema_To_v1alpha2_ResourceSchema(ae.Spec.LatestResourceSchemas, &v2Schemas)
		if err != nil {
			return field.Invalid(field.NewPath("spec").Child("latestResourceSchemas"), ae.Spec.LatestResourceSchemas, "failed to convert spec.LatestResourceSchema")
		}
		overhanging = append(overhanging, v2Schemas...)

		seen := map[string]struct{}{}
		for _, rs := range overhanging {
			if _, ok := seen[rs.Schema]; ok {
				return field.Invalid(field.NewPath("metadata").Child("annotations").Key(apisv1alpha2.ResourceSchemasAnnotation), ae.Annotations[apisv1alpha2.ResourceSchemasAnnotation], "duplicate resource schema")
			}
			seen[rs.Schema] = struct{}{}
		}
	}
	return nil
}

func validateOverhangingPermissionClaims(_ context.Context, _ admission.Attributes, ae *apisv1alpha1.APIExport) error {
	// TODO(xmudrii): Remove this once we are sure that all APIExport objects are
	// converted to v1alpha2.
	if _, ok := ae.Annotations[apisv1alpha2.PermissionClaimsAnnotation]; ok {
		// validate if we can decode overhanging permission claims. If not, we will fail.
		var overhanging []apisv1alpha2.PermissionClaim
		if err := json.Unmarshal([]byte(ae.Annotations[apisv1alpha2.PermissionClaimsAnnotation]), &overhanging); err != nil {
			return field.Invalid(field.NewPath("metadata").Child("annotations").Key(apisv1alpha2.PermissionClaimsAnnotation), ae.Annotations[apisv1alpha2.PermissionClaimsAnnotation], "failed to decode overhanging permission claims")
		}

		// validate mismatches. We could have mismatches between the spec and the annotation
		// (e.g. a resource present in the annotation, but not in the spec).
		// We convert to v2 to check for mismatches.
		v2Claims := make([]apisv1alpha2.PermissionClaim, len(ae.Spec.PermissionClaims))
		for i, v1pc := range ae.Spec.PermissionClaims {
			var v2pc apisv1alpha2.PermissionClaim
			err := apisv1alpha2.Convert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim(&v1pc, &v2pc, nil)
			if err != nil {
				return field.Invalid(field.NewPath("spec").Child("permissionClaims").Index(i), ae.Spec.PermissionClaims, "failed to convert spec.PermissionClaims")
			}
			v2Claims = append(v2Claims, v2pc)
		}

		for _, o := range overhanging {
			var found bool
			for _, pc := range v2Claims {
				if pc.EqualGRI(o) {
					found = true

					break
				}
			}
			if !found {
				return field.Invalid(field.NewPath("metadata").Child("annotations").Key(apisv1alpha2.PermissionClaimsAnnotation), ae.Annotations[apisv1alpha2.PermissionClaimsAnnotation], "permission claims defined in annotation do not match permission claims defined in spec")
			}
		}
	}
	return nil
}
