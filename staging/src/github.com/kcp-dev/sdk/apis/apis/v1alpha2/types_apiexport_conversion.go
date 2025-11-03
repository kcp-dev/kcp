/*
Copyright 2025 The KCP Authors.

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

package v1alpha2

import (
	"encoding/json"
	"fmt"
	"strings"

	kubeconversion "k8s.io/apimachinery/pkg/conversion"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

const (
	ResourceSchemasAnnotation          = "apis.v1alpha2.kcp.io/resource-schemas"
	PermissionClaimsAnnotation         = "apis.v1alpha2.kcp.io/permission-claims"
	PermissionClaimsV1Alpha1Annotation = "apis.v1alpha2.kcp.io/v1alpha1-permission-claims"
)

// v1alpha2 -> v1alpha1 conversions.

func Convert_v1alpha2_APIExport_To_v1alpha1_APIExport(in *APIExport, out *apisv1alpha1.APIExport, s kubeconversion.Scope) error {
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()

	// Before converting the spec, figure out which ResourceSchemas could not be represented in v1alpha1 and
	// retain them via an annotation.
	_, overhangingRS := Convert_v1alpha2_ResourceSchemas_To_v1alpha1_LatestResourceSchemas(in.Spec)
	if len(overhangingRS) > 0 {
		encoded, err := json.Marshal(overhangingRS)
		if err != nil {
			return fmt.Errorf("failed to encode schemas as JSON: %w", err)
		}
		if out.Annotations == nil {
			out.Annotations = map[string]string{}
		}
		out.Annotations[ResourceSchemasAnnotation] = string(encoded)
	}

	// Before converting the spec, figure out which PermissionClaims could not be represented in v1alpha1 and
	// retain them via an annotation.
	_, overhangingPC, err := Convert_v1alpha2_PermissionClaims_To_v1alpha1_PermissionClaims(in.Spec.PermissionClaims, s)
	if err != nil {
		return err
	}
	if len(overhangingPC) > 0 {
		encoded, err := json.Marshal(overhangingPC)
		if err != nil {
			return fmt.Errorf("failed to encode claims as JSON: %w", err)
		}

		if out.Annotations == nil {
			out.Annotations = map[string]string{}
		}
		out.Annotations[PermissionClaimsAnnotation] = string(encoded)
	}

	if err := Convert_v1alpha2_APIExportSpec_To_v1alpha1_APIExportSpec(&in.Spec, &out.Spec, s); err != nil {
		return err
	}

	// If the object was converted from v1alpha1 to v1alpha2 to v1alpha1, retain All and ResourceSelector
	// fields in PermissionClaims by reading the original (v1alpha1) PermissionClaims from an annotation.
	if originalPermissionClaims, ok := in.Annotations[PermissionClaimsV1Alpha1Annotation]; ok {
		permissionClaims := []apisv1alpha1.PermissionClaim{}
		if err := json.Unmarshal([]byte(originalPermissionClaims), &permissionClaims); err != nil {
			return fmt.Errorf("failed to decode v1alpha1 claims from JSON: %w", err)
		}

		for _, pc := range permissionClaims {
			for i, opc := range out.Spec.PermissionClaims {
				if pc.EqualGRI(opc) {
					out.Spec.PermissionClaims[i].All = pc.All
					out.Spec.PermissionClaims[i].ResourceSelector = pc.ResourceSelector
				}
			}
		}

		delete(out.Annotations, PermissionClaimsV1Alpha1Annotation)

		// Make tests for equality easier to write by turning []string into nil.
		if len(out.Annotations) == 0 {
			out.Annotations = nil
		}
	}

	return Convert_v1alpha2_APIExportStatus_To_v1alpha1_APIExportStatus(&in.Status, &out.Status, s)
}

func Convert_v1alpha2_ResourceSchemas_To_v1alpha1_LatestResourceSchemas(in APIExportSpec) ([]string, []ResourceSchema) {
	hubSchemas := []string{}
	nonCRDSchemas := []ResourceSchema{}

	if schemas := in.Resources; schemas != nil {
		for _, schema := range schemas {
			if schema.Storage.CRD != nil {
				hubSchemas = append(hubSchemas, schema.Schema)
			} else {
				nonCRDSchemas = append(nonCRDSchemas, schema)
			}
		}
	}

	return hubSchemas, nonCRDSchemas
}

func Convert_v1alpha2_PermissionClaims_To_v1alpha1_PermissionClaims(in []PermissionClaim, s kubeconversion.Scope) ([]apisv1alpha1.PermissionClaim, []PermissionClaim, error) {
	var (
		wildcardClaims    []apisv1alpha1.PermissionClaim
		nonWildcardClaims []PermissionClaim
	)

	for _, pc := range in {
		if len(pc.Verbs) == 1 && pc.Verbs[0] == "*" {
			var v1pc apisv1alpha1.PermissionClaim
			err := Convert_v1alpha2_PermissionClaim_To_v1alpha1_PermissionClaim(&pc, &v1pc, s)
			if err != nil {
				return nil, nil, err
			}

			wildcardClaims = append(wildcardClaims, v1pc)
		} else {
			nonWildcardClaims = append(nonWildcardClaims, pc)
		}
	}

	return wildcardClaims, nonWildcardClaims, nil
}

// Convert_v1alpha2_APIExportSpec_To_v1alpha1_APIExportSpec is *not* lossless, as it will drop all non-CRD
// resource schemas and all non-wildcard PermissionClaims present in the APIExport's spec.
// To have a full, lossless conversion, use Convert_v1alpha2_APIExport_To_v1alpha1_APIExport instead.
func Convert_v1alpha2_APIExportSpec_To_v1alpha1_APIExportSpec(in *APIExportSpec, out *apisv1alpha1.APIExportSpec, s kubeconversion.Scope) error {
	if in.Identity != nil {
		out.Identity = &apisv1alpha1.Identity{}
		if err := Convert_v1alpha2_Identity_To_v1alpha1_Identity(in.Identity, out.Identity, s); err != nil {
			return err
		}
	}

	if in.MaximalPermissionPolicy != nil {
		out.MaximalPermissionPolicy = &apisv1alpha1.MaximalPermissionPolicy{}
		if err := Convert_v1alpha2_MaximalPermissionPolicy_To_v1alpha1_MaximalPermissionPolicy(in.MaximalPermissionPolicy, out.MaximalPermissionPolicy, s); err != nil {
			return err
		}
	}

	if claims := in.PermissionClaims; claims != nil {
		newClaims := []apisv1alpha1.PermissionClaim{}
		for _, claim := range claims {
			var newClaim apisv1alpha1.PermissionClaim
			if err := Convert_v1alpha2_PermissionClaim_To_v1alpha1_PermissionClaim(&claim, &newClaim, s); err != nil {
				return err
			}
			newClaims = append(newClaims, newClaim)
		}
		out.PermissionClaims = newClaims
	}

	latest, _ := Convert_v1alpha2_ResourceSchemas_To_v1alpha1_LatestResourceSchemas(*in)
	if len(latest) > 0 {
		out.LatestResourceSchemas = latest
	}

	return nil
}

// Convert_v1alpha2_PermissionClaim_To_v1alpha1_PermissionClaim ensures we do the default conversion for
// PermissionClaims. Verbs are ignored in this phase and are handled in
// Convert_v1alpha2_APIExport_To_v1alpha1_APIExport.
func Convert_v1alpha2_PermissionClaim_To_v1alpha1_PermissionClaim(in *PermissionClaim, out *apisv1alpha1.PermissionClaim, s kubeconversion.Scope) error {
	return autoConvert_v1alpha2_PermissionClaim_To_v1alpha1_PermissionClaim(in, out, s)
}

// v1alpha1 -> v1alpha2 conversions.

func Convert_v1alpha1_APIExport_To_v1alpha2_APIExport(in *apisv1alpha1.APIExport, out *APIExport, s kubeconversion.Scope) error {
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()

	if err := Convert_v1alpha1_APIExportSpec_To_v1alpha2_APIExportSpec(&in.Spec, &out.Spec, s); err != nil {
		return err
	}
	if err := Convert_v1alpha1_APIExportStatus_To_v1alpha2_APIExportStatus(&in.Status, &out.Status, s); err != nil {
		return err
	}

	// Store permission claims in annotation. This is necessary for a clean conversion of
	// fields All and FieldSelector, which went away in v1alpha2.
	if len(in.Spec.PermissionClaims) > 0 {
		if out.Annotations == nil {
			out.Annotations = make(map[string]string)
		}
		encoded, err := json.Marshal(in.Spec.PermissionClaims)
		if err != nil {
			return err
		}
		out.Annotations[PermissionClaimsV1Alpha1Annotation] = string(encoded)
	}

	if overhangingRS, ok := in.Annotations[ResourceSchemasAnnotation]; ok {
		resourceSchemas := []ResourceSchema{}
		if err := json.Unmarshal([]byte(overhangingRS), &resourceSchemas); err != nil {
			return fmt.Errorf("failed to decode schemas from JSON: %w", err)
		}

		if len(resourceSchemas) > 0 {
			if out.Spec.Resources == nil {
				out.Spec.Resources = []ResourceSchema{}
			}

			out.Spec.Resources = append(out.Spec.Resources, resourceSchemas...)
		}

		delete(out.Annotations, ResourceSchemasAnnotation)

		// Make tests for equality easier to write by turning []string into nil.
		if len(out.Annotations) == 0 {
			out.Annotations = nil
		}
	}

	if overhangingPC, ok := in.Annotations[PermissionClaimsAnnotation]; ok {
		permissionClaims := []PermissionClaim{}
		if err := json.Unmarshal([]byte(overhangingPC), &permissionClaims); err != nil {
			return fmt.Errorf("failed to decode claims from JSON: %w", err)
		}

		if len(permissionClaims) > 0 {
			if out.Spec.PermissionClaims == nil {
				out.Spec.PermissionClaims = []PermissionClaim{}
			}

			for _, pc := range permissionClaims {
				for i, opc := range out.Spec.PermissionClaims {
					if pc.EqualGRI(opc) {
						out.Spec.PermissionClaims[i].Verbs = pc.Verbs
					}
				}
			}
		}

		delete(out.Annotations, PermissionClaimsAnnotation)

		// Make tests for equality easier to write by turning []string into nil.
		if len(out.Annotations) == 0 {
			out.Annotations = nil
		}
	}

	for i, opc := range out.Spec.PermissionClaims {
		if len(opc.Verbs) == 0 {
			out.Spec.PermissionClaims[i].Verbs = []string{"*"}
		}
	}

	return nil
}

func Convert_v1alpha1_APIExportSpec_To_v1alpha2_APIExportSpec(in *apisv1alpha1.APIExportSpec, out *APIExportSpec, s kubeconversion.Scope) error {
	if in.Identity != nil {
		out.Identity = &Identity{}
		if err := Convert_v1alpha1_Identity_To_v1alpha2_Identity(in.Identity, out.Identity, s); err != nil {
			return err
		}
	}

	if in.MaximalPermissionPolicy != nil {
		out.MaximalPermissionPolicy = &MaximalPermissionPolicy{}
		if err := Convert_v1alpha1_MaximalPermissionPolicy_To_v1alpha2_MaximalPermissionPolicy(in.MaximalPermissionPolicy, out.MaximalPermissionPolicy, s); err != nil {
			return err
		}
	}

	// This will not convert non-wildcard PermissionClaims verbs and All/ResourceSelector fields. Those are still
	// tucked away in an annotation and are converted after this function has completed, in
	// Convert_v1alpha1_APIExport_To_v1alpha2_APIExport.
	if claims := in.PermissionClaims; claims != nil {
		newClaims := []PermissionClaim{}
		for _, claim := range claims {
			var newClaim PermissionClaim
			if err := Convert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim(&claim, &newClaim, s); err != nil {
				return err
			}
			newClaims = append(newClaims, newClaim)
		}
		out.PermissionClaims = newClaims
	}

	// This will only convert CRD-based ResourceSchemas. All others are still tucked away in an annotation
	// and are converted after this function has completed, in Convert_v1alpha1_APIExport_To_v1alpha2_APIExport.
	if schemas := in.LatestResourceSchemas; schemas != nil {
		newSchemas := []ResourceSchema{}
		for _, schema := range schemas {
			// parse strings like "v1.resource.group.org"
			parts := strings.Split(schema, ".")
			if len(parts) < 3 {
				return fmt.Errorf("invalid schema %q: must have at least 3 dot-separated segments", schema)
			}

			resource := parts[1]
			group := strings.Join(parts[2:], ".")
			if group == "core" {
				group = ""
			}

			newSchemas = append(newSchemas, ResourceSchema{
				Group:  group,
				Name:   resource,
				Schema: schema,
				Storage: ResourceSchemaStorage{
					CRD: &ResourceSchemaStorageCRD{},
				},
			})
		}

		out.Resources = newSchemas
	}

	return nil
}

// Convert_v1alpha1_LatestResourceSchema_To_v1alpha2_ResourceSchema will only convert CRD-based ResourceSchemas. All
// others are still tucked away in an annotation and are converted after this function has completed,
// in Convert_v1alpha1_APIExport_To_v1alpha2_APIExport.
func Convert_v1alpha1_LatestResourceSchema_To_v1alpha2_ResourceSchema(in []string, out *[]ResourceSchema) error {
	if out == nil {
		return fmt.Errorf("output slice is nil. Programmer error")
	}
	// This will only convert CRD-based ResourceSchemas. All others are still tucked away in an annotation
	// and are converted after this function has completed, in Convert_v1alpha1_APIExport_To_v1alpha2_APIExport.
	if schemas := in; schemas != nil {
		for _, schema := range schemas {
			// Parse strings like "v1.resource.group.org".
			parts := strings.Split(schema, ".")
			if len(parts) < 3 {
				return fmt.Errorf("invalid schema %q: must have at least 3 dot-separated segments", schema)
			}

			resource := parts[1]
			group := strings.Join(parts[2:], ".")
			if group == "core" {
				group = ""
			}

			*out = append(*out, ResourceSchema{
				Group:  group,
				Name:   resource,
				Schema: schema,
				Storage: ResourceSchemaStorage{
					CRD: &ResourceSchemaStorageCRD{},
				},
			})
		}

		return nil
	}
	return nil
}

// Convert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim ensures we do the default conversion for
// PermissionClaims. Fields "All", "ResourceSelector", and "Verbs" are stored in relevant annotations.
func Convert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim(in *apisv1alpha1.PermissionClaim, out *PermissionClaim, s kubeconversion.Scope) error {
	return autoConvert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim(in, out, s)
}
