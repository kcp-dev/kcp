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
	kubeconversion "k8s.io/apimachinery/pkg/conversion"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

func (src *APIExport) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*apisv1alpha1.APIExport)

	s := runtime.NewScheme()
	localSchemeBuilder.AddToScheme(s)

	return s.Convert(src, dst, nil)
}

func (dst *APIExport) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*apisv1alpha1.APIExport)

	s := runtime.NewScheme()
	localSchemeBuilder.AddToScheme(s)

	return s.Convert(src, dst, nil)
}

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

	if schemas := in.ResourceSchemas; schemas != nil {
		hubSchemas := []string{}
		for _, schema := range schemas {
			// TODO: Should this return an error if non-CRD-based resource schemas are used, or
			// resort to an annotation?
			hubSchemas = append(hubSchemas, schema.Schema)
		}

		out.LatestResourceSchemas = hubSchemas
	}

	return nil
}

func Convert_v1alpha1_APIExportSpec_To_v1alpha2_APIExportSpec(in *apisv1alpha1.APIExportSpec, out *APIExportSpec, s kubeconversion.Scope) error {
	if err := Convert_v1alpha1_Identity_To_v1alpha2_Identity(in.Identity, out.Identity, s); err != nil {
		return err
	}
	if err := Convert_v1alpha1_MaximalPermissionPolicy_To_v1alpha2_MaximalPermissionPolicy(in.MaximalPermissionPolicy, out.MaximalPermissionPolicy, s); err != nil {
		return err
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
