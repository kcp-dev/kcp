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

package forwardingregistry

import (
	"context"
	"fmt"
	"math"
	"strings"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/kube-openapi/pkg/validation/validate"
)

type validator struct {
	namespaceScoped       bool
	kind                  schema.GroupVersionKind
	schemaValidator       *validate.SchemaValidator
	statusSchemaValidator *validate.SchemaValidator
}

func (a validator) Validate(ctx context.Context, obj runtime.Object) field.ErrorList {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return field.ErrorList{field.Invalid(field.NewPath(""), u, fmt.Sprintf("has type %T. Must be a pointer to an Unstructured type", u))}
	}
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return field.ErrorList{field.Invalid(field.NewPath("metadata"), nil, err.Error())}
	}

	if errs := a.ValidateTypeMeta(ctx, u); len(errs) > 0 {
		return errs
	}

	var allErrs field.ErrorList

	allErrs = append(allErrs, validation.ValidateObjectMetaAccessor(accessor, a.namespaceScoped, validation.NameIsDNSSubdomain, field.NewPath("metadata"))...)
	allErrs = append(allErrs, apiservervalidation.ValidateCustomResource(nil, u.UnstructuredContent(), a.schemaValidator)...)

	return allErrs
}

func (a validator) ValidateUpdate(ctx context.Context, obj, old runtime.Object) field.ErrorList {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return field.ErrorList{field.Invalid(field.NewPath(""), u, fmt.Sprintf("has type %T. Must be a pointer to an Unstructured type", u))}
	}
	objAccessor, err := meta.Accessor(obj)
	if err != nil {
		return field.ErrorList{field.Invalid(field.NewPath("metadata"), nil, err.Error())}
	}
	oldAccessor, err := meta.Accessor(old)
	if err != nil {
		return field.ErrorList{field.Invalid(field.NewPath("metadata"), nil, err.Error())}
	}

	if errs := a.ValidateTypeMeta(ctx, u); len(errs) > 0 {
		return errs
	}

	var allErrs field.ErrorList

	allErrs = append(allErrs, validation.ValidateObjectMetaAccessorUpdate(objAccessor, oldAccessor, field.NewPath("metadata"))...)
	allErrs = append(allErrs, apiservervalidation.ValidateCustomResource(nil, u.UnstructuredContent(), a.schemaValidator)...)

	return allErrs
}

// WarningsOnUpdate returns warnings for the given update.
func (validator) WarningsOnUpdate(ctx context.Context, obj, old runtime.Object) []string {
	return nil
}

func (a validator) ValidateStatusUpdate(ctx context.Context, obj, old runtime.Object, scale *apiextensions.CustomResourceSubresourceScale) field.ErrorList {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return field.ErrorList{field.Invalid(field.NewPath(""), u, fmt.Sprintf("has type %T. Must be a pointer to an Unstructured type", u))}
	}
	objAccessor, err := meta.Accessor(obj)
	if err != nil {
		return field.ErrorList{field.Invalid(field.NewPath("metadata"), nil, err.Error())}
	}
	oldAccessor, err := meta.Accessor(old)
	if err != nil {
		return field.ErrorList{field.Invalid(field.NewPath("metadata"), nil, err.Error())}
	}

	if errs := a.ValidateTypeMeta(ctx, u); len(errs) > 0 {
		return errs
	}

	var allErrs field.ErrorList

	allErrs = append(allErrs, validation.ValidateObjectMetaAccessorUpdate(objAccessor, oldAccessor, field.NewPath("metadata"))...)
	allErrs = append(allErrs, apiservervalidation.ValidateCustomResource(nil, u.UnstructuredContent(), a.schemaValidator)...)
	allErrs = append(allErrs, a.ValidateScaleStatus(ctx, u, scale)...)

	return allErrs
}

func (a validator) ValidateTypeMeta(ctx context.Context, obj *unstructured.Unstructured) field.ErrorList {
	typeAccessor, err := meta.TypeAccessor(obj)
	if err != nil {
		return field.ErrorList{field.Invalid(field.NewPath("kind"), nil, err.Error())}
	}

	var allErrs field.ErrorList
	if typeAccessor.GetKind() != a.kind.Kind {
		allErrs = append(allErrs, field.Invalid(field.NewPath("kind"), typeAccessor.GetKind(), fmt.Sprintf("must be %v", a.kind.Kind)))
	}
	// HACK: support the case when we add core resources through CRDs (KCP scenario)
	expectedAPIVersion := a.kind.Group + "/" + a.kind.Version
	if a.kind.Group == "" {
		expectedAPIVersion = a.kind.Version
	}
	if typeAccessor.GetAPIVersion() != expectedAPIVersion {
		allErrs = append(allErrs, field.Invalid(field.NewPath("apiVersion"), typeAccessor.GetAPIVersion(), fmt.Sprintf("must be %v", expectedAPIVersion)))
	}
	return allErrs
}

func (a validator) ValidateScaleStatus(ctx context.Context, obj *unstructured.Unstructured, scale *apiextensions.CustomResourceSubresourceScale) field.ErrorList {
	if scale == nil {
		return nil
	}

	var allErrs field.ErrorList

	// validate statusReplicas
	statusReplicasPath := strings.TrimPrefix(scale.StatusReplicasPath, ".") // ignore leading period
	statusReplicas, _, err := unstructured.NestedInt64(obj.UnstructuredContent(), strings.Split(statusReplicasPath, ".")...)
	if err != nil {
		allErrs = append(allErrs, field.Invalid(field.NewPath(scale.StatusReplicasPath), statusReplicas, err.Error()))
	} else if statusReplicas < 0 {
		allErrs = append(allErrs, field.Invalid(field.NewPath(scale.StatusReplicasPath), statusReplicas, "must be a non-negative integer"))
	} else if statusReplicas > math.MaxInt32 {
		allErrs = append(allErrs, field.Invalid(field.NewPath(scale.StatusReplicasPath), statusReplicas, fmt.Sprintf("must be less than or equal to %v", math.MaxInt32)))
	}

	// validate labelSelector
	if scale.LabelSelectorPath != nil {
		labelSelectorPath := strings.TrimPrefix(*scale.LabelSelectorPath, ".") // ignore leading period
		labelSelector, _, err := unstructured.NestedString(obj.UnstructuredContent(), strings.Split(labelSelectorPath, ".")...)
		if err != nil {
			allErrs = append(allErrs, field.Invalid(field.NewPath(*scale.LabelSelectorPath), labelSelector, err.Error()))
		}
	}

	return allErrs
}
