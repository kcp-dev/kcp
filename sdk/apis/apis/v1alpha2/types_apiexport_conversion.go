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

package v1alpha2

import (
	"encoding/json"
	"fmt"

	kubeconversion "k8s.io/apimachinery/pkg/conversion"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

const (
	resourceSchemasAnnotation = "apis.v1alpha2.kcp.io/resource-schemas"
)

func Convert_v1alpha2_APIExport_To_v1alpha1_APIExport(in *APIExport, out *apisv1alpha1.APIExport, s kubeconversion.Scope) error {
	out.ObjectMeta = in.ObjectMeta

	// before converting the spec, figure out which ResourceSchemas could not be represented in v1alpha1 and
	// retain them via an annotation
	_, overhanging := convert_v1alpha2_ResourceSchemas_To_v1alpha1_LatestResourceSchemas(in.Spec)
	if len(overhanging) > 0 {
		encoded, err := json.Marshal(overhanging)
		if err != nil {
			return fmt.Errorf("failed to encode schemas as JSON: %w", err)
		}
		if out.Annotations == nil {
			out.Annotations = map[string]string{}
		}
		out.Annotations[resourceSchemasAnnotation] = string(encoded)
	}

	if err := Convert_v1alpha2_APIExportSpec_To_v1alpha1_APIExportSpec(&in.Spec, &out.Spec, s); err != nil {
		return err
	}
	if err := Convert_v1alpha2_APIExportStatus_To_v1alpha1_APIExportStatus(&in.Status, &out.Status, s); err != nil {
		return err
	}

	return nil
}

func convert_v1alpha2_ResourceSchemas_To_v1alpha1_LatestResourceSchemas(in APIExportSpec) ([]string, []ResourceSchema) {
	hubSchemas := []string{}
	nonCRDSchemas := []ResourceSchema{}

	if schemas := in.ResourceSchemas; schemas != nil {
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

// Convert_v1alpha2_APIExportSpec_To_v1alpha1_APIExportSpec is *not* lossless, as it will drop all non-CRD
// resource schemas present in the APIExport's spec. To have a full, lossless conversion, use
// Convert_v1alpha2_APIExport_To_v1alpha1_APIExport instead.
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

	latest, _ := convert_v1alpha2_ResourceSchemas_To_v1alpha1_LatestResourceSchemas(*in)
	if len(latest) > 0 {
		out.LatestResourceSchemas = latest
	}

	return nil
}

func Convert_v1alpha1_APIExport_To_v1alpha2_APIExport(in *apisv1alpha1.APIExport, out *APIExport, s kubeconversion.Scope) error {
	if err := autoConvert_v1alpha1_APIExport_To_v1alpha2_APIExport(in, out, s); err != nil {
		return err
	}

	if overhanging, ok := in.Annotations[resourceSchemasAnnotation]; ok {
		resourceSchemas := []ResourceSchema{}
		if err := json.Unmarshal([]byte(overhanging), &resourceSchemas); err != nil {
			return fmt.Errorf("failed to decode schemas from JSON: %w", err)
		}

		if len(resourceSchemas) > 0 {
			if out.Spec.ResourceSchemas == nil {
				out.Spec.ResourceSchemas = []ResourceSchema{}
			}

			out.Spec.ResourceSchemas = append(out.Spec.ResourceSchemas, resourceSchemas...)
		}

		delete(out.Annotations, resourceSchemasAnnotation)

		// make tests for equality easier to write by turning []string into nil
		if len(out.Annotations) == 0 {
			out.Annotations = nil
		}
	}

	return nil
}

func Convert_v1alpha1_APIExportSpec_To_v1alpha2_APIExportSpec(in *apisv1alpha1.APIExportSpec, out *APIExportSpec, s kubeconversion.Scope) error {
	if in.Identity != nil {
		if err := Convert_v1alpha1_Identity_To_v1alpha2_Identity(in.Identity, out.Identity, s); err != nil {
			return err
		}
	}

	if in.MaximalPermissionPolicy != nil {
		if err := Convert_v1alpha1_MaximalPermissionPolicy_To_v1alpha2_MaximalPermissionPolicy(in.MaximalPermissionPolicy, out.MaximalPermissionPolicy, s); err != nil {
			return err
		}
	}

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
			newSchemas = append(newSchemas, ResourceSchema{
				Schema: schema,
				Storage: ResourceSchemaStorage{
					CRD: &ResourceSchemaStorageCRD{},
				},
			})
		}

		out.ResourceSchemas = newSchemas
	}

	return nil
}
