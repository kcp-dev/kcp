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

package apiresourceschema

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	crdvalidation "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/sets"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/cel/environment"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

var (
	namePrefixRE                 = regexp.MustCompile("^[a-z]([-a-z0-9]*[a-z0-9])?$")
	singleSegmentGroupExceptions = sets.New[string]("apps", "batch", "extensions", "policy", "autoscaling") // these are the sins of Kubernetes of single-word group names
)

// ValidateAPIResourceSchema validates an APIResourceSchema.
func ValidateAPIResourceSchema(ctx context.Context, s *apisv1alpha1.APIResourceSchema) field.ErrorList {
	allErrs := field.ErrorList{}
	allErrs = append(allErrs, ValidateAPIResourceSchemaName(s.Name, &s.Spec, field.NewPath("metadata", "name"))...)
	allErrs = append(allErrs, ValidateAPIResourceSchemaGroup(s.Spec.Group, s.Annotations)...)
	allErrs = append(allErrs, ValidateAPIResourceSchemaSpec(ctx, &s.Spec, field.NewPath("spec"))...)
	return allErrs
}

func ValidateAPIResourceSchemaName(name string, spec *apisv1alpha1.APIResourceSchemaSpec, path *field.Path) field.ErrorList {
	// all this is in addition to CRD default name validation.

	if spec.Names.Plural != "" {
		// if the plural is not set, names validation will fail. We avoid a strange dot-dot-group message here.

		group := spec.Group
		if group == "" {
			group = "core"
		}
		expectedNamePostfix := fmt.Sprintf(".%s.%s", spec.Names.Plural, group)
		if !strings.HasSuffix(name, expectedNamePostfix) {
			return field.ErrorList{field.Invalid(path, name, fmt.Sprintf("must end in %s", expectedNamePostfix))}
		}

		rest := strings.TrimSuffix(name, expectedNamePostfix)
		if !namePrefixRE.MatchString(rest) {
			return field.ErrorList{field.Invalid(path, name, fmt.Sprintf("must match ^[a-z]([-a-z0-9]*[a-z0-9])?$ in front of .%s.%s", spec.Names.Plural, group))}
		}
	}

	return nil
}

func ValidateAPIResourceSchemaGroup(group string, annotations map[string]string) field.ErrorList {
	// check to see if we need confirm API approval for kube group.
	if !apihelpers.IsProtectedCommunityGroup(group) {
		// no-op for non-protected groups
		return nil
	}

	state, reason := apihelpers.GetAPIApprovalState(annotations)

	switch state {
	case apihelpers.APIApprovalInvalid:
		return field.ErrorList{field.Invalid(field.NewPath("metadata", "annotations").Key(apiextensionsv1beta1.KubeAPIApprovedAnnotation), annotations[apiextensionsv1beta1.KubeAPIApprovedAnnotation], reason)}
	case apihelpers.APIApprovalMissing:
		return field.ErrorList{field.Required(field.NewPath("metadata", "annotations").Key(apiextensionsv1beta1.KubeAPIApprovedAnnotation), reason)}
	case apihelpers.APIApproved, apihelpers.APIApprovalBypassed:
		// success
		return nil
	default:
		return field.ErrorList{field.Invalid(field.NewPath("metadata", "annotations").Key(apiextensionsv1beta1.KubeAPIApprovedAnnotation), annotations[apiextensionsv1beta1.KubeAPIApprovedAnnotation], reason)}
	}
}

func ValidateAPIResourceSchemaSpec(ctx context.Context, spec *apisv1alpha1.APIResourceSchemaSpec, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	// HACK: Relax naming constraints when registering legacy schema resources through CRDs
	// for the KCP scenario
	if spec.Group == "" {
		// pass. This is the core group
	} else if spec.Group == "core" {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("group"), spec.Group, "must be empty string for the core group"))
	} else if errs := utilvalidation.IsDNS1123Subdomain(spec.Group); len(errs) > 0 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("group"), spec.Group, strings.Join(errs, ",")))
	} else if len(strings.Split(spec.Group, ".")) < 2 && !singleSegmentGroupExceptions.Has(spec.Group) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("group"), spec.Group, "should be a domain with at least one dot"))
	}

	storageFlagCount := 0
	versionsMap := map[string]bool{}
	uniqueNames := true
	for i := range spec.Versions {
		version := spec.Versions[i]
		if version.Storage {
			storageFlagCount++
		}
		if versionsMap[version.Name] {
			uniqueNames = false
		} else {
			versionsMap[version.Name] = true
		}
		if errs := utilvalidation.IsDNS1035Label(version.Name); len(errs) > 0 {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("versions").Index(i).Child("name"), spec.Versions[i].Name, strings.Join(errs, ",")))
		}
		allErrs = append(allErrs, ValidateAPIResourceVersion(ctx, &version, fldPath.Child("versions").Index(i))...)
	}
	if !uniqueNames {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("versions"), spec.Versions, "must contain unique version names"))
	}
	if storageFlagCount != 1 {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("versions"), spec.Versions, "must have exactly one version marked as storage version"))
	}

	// in addition to the basic name restrictions, some names are required for spec, but not for status
	if len(spec.Names.Plural) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("names", "plural"), ""))
	}
	if len(spec.Names.Singular) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("names", "singular"), ""))
	}
	if len(spec.Names.Kind) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("names", "kind"), ""))
	}
	if len(spec.Names.ListKind) == 0 {
		allErrs = append(allErrs, field.Required(fldPath.Child("names", "listKind"), ""))
	}

	var crdNames apiextensionsinternal.CustomResourceDefinitionNames
	if err := apiextensionsv1.Convert_v1_CustomResourceDefinitionNames_To_apiextensions_CustomResourceDefinitionNames(&spec.Names, &crdNames, nil); err != nil {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("names"), spec.Names, err.Error()))
	} else {
		allErrs = append(allErrs, crdvalidation.ValidateCustomResourceDefinitionNames(&crdNames, fldPath.Child("names"))...)
	}

	// TODO(sttts): validate predecessors

	return allErrs
}

var defaultValidationOpts = crdvalidation.ValidationOptions{
	AllowDefaults:                            true,
	RequireRecognizedConversionReviewVersion: true,

	// in Kube this becomes true after Established=true.
	// Here this does not matter. The whole resource is always immutable.
	RequireImmutableNames: false,

	RequireOpenAPISchema:               true,
	RequireValidPropertyType:           true,
	RequireStructuralSchema:            true,
	RequirePrunedDefaults:              true,
	RequireAtomicSetType:               true,
	RequireMapListKeysMapSetValidation: true,

	CELEnvironmentSet: environment.MustBaseEnvSet(environment.DefaultCompatibilityVersion()),
}

func ValidateAPIResourceVersion(ctx context.Context, version *apisv1alpha1.APIResourceVersion, fldPath *field.Path) field.ErrorList {
	allErrs := field.ErrorList{}

	for _, err := range crdvalidation.ValidateDeprecationWarning(version.Deprecated, version.DeprecationWarning) {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("deprecationWarning"), version.DeprecationWarning, err))
	}

	if len(version.Schema.Raw) == 0 || string(version.Schema.Raw) == "null" {
		allErrs = append(allErrs, field.Required(fldPath.Child("schema"), ""))
	} else {
		statusEnabled := version.Subresources.Status != nil
		var crdSchemaV1 apiextensionsv1.CustomResourceValidation
		var crdSchemaInternal apiextensionsinternal.CustomResourceValidation
		if err := json.Unmarshal(version.Schema.Raw, &crdSchemaV1.OpenAPIV3Schema); err != nil {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("schema"), string(version.Schema.Raw), fmt.Sprintf("invalid JSON: %v", err)))
		} else if err := apiextensionsv1.Convert_v1_CustomResourceValidation_To_apiextensions_CustomResourceValidation(&crdSchemaV1, &crdSchemaInternal, nil); err != nil {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("schema"), string(version.Schema.Raw), fmt.Sprintf("invalid schema: %v", err)))
		} else {
			allErrs = append(allErrs, crdvalidation.ValidateCustomResourceDefinitionValidation(ctx, &crdSchemaInternal, statusEnabled, defaultValidationOpts, fldPath.Child("schema"))...)
		}
	}

	var crdSubresources apiextensionsinternal.CustomResourceSubresources
	if err := apiextensionsv1.Convert_v1_CustomResourceSubresources_To_apiextensions_CustomResourceSubresources(&version.Subresources, &crdSubresources, nil); err != nil {
		allErrs = append(allErrs, field.Invalid(fldPath.Child("subresources"), version.Subresources, err.Error()))
	} else {
		allErrs = append(allErrs, crdvalidation.ValidateCustomResourceDefinitionSubresources(&crdSubresources, fldPath.Child("subresources"))...)
	}

	for i := range version.AdditionalPrinterColumns {
		var crdColumn apiextensionsinternal.CustomResourceColumnDefinition
		if err := apiextensionsv1.Convert_v1_CustomResourceColumnDefinition_To_apiextensions_CustomResourceColumnDefinition(&version.AdditionalPrinterColumns[i], &crdColumn, nil); err != nil {
			allErrs = append(allErrs, field.Invalid(fldPath.Child("additionalPrinterColumns").Index(i), version.Subresources, err.Error()))
		} else {
			allErrs = append(allErrs, crdvalidation.ValidateCustomResourceColumnDefinition(&crdColumn, fldPath.Child("additionalPrinterColumns").Index(i))...)
		}
	}

	return allErrs
}

// ValidateAPIResourceSchemaUpdate validates an APIResourceSchema on update.
func ValidateAPIResourceSchemaUpdate(ctx context.Context, s, old *apisv1alpha1.APIResourceSchema) field.ErrorList {
	allErrs := ValidateAPIResourceSchema(ctx, s)

	if !equality.Semantic.DeepEqual(old.Spec, s.Spec) {
		allErrs = append(allErrs, field.Invalid(field.NewPath("spec"), s.Spec, "is immutable"))
	}

	return allErrs
}
