/*
Copyright 2023 The KCP Authors.

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

package conversion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/cel-go/interpreter"
	"github.com/prometheus/client_golang/prometheus"

	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	structuraldefaulting "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	compbasemetrics "k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

func init() {
	legacyregistry.MustRegister(celTransformationDuration)
}

var (
	celTransformationDuration = compbasemetrics.NewHistogramVec(
		&compbasemetrics.HistogramOpts{
			Name: "conversion_cel_transformation_duration_seconds",
			Help: "CEL transformation execution time distribution in seconds",
			// From .001 to 16.384 seconds
			Buckets:        prometheus.ExponentialBuckets(.001, 2, 15),
			StabilityLevel: compbasemetrics.ALPHA,
		},
		[]string{},
	)
)

func NewConverter(
	crd *apiextensionsv1.CustomResourceDefinition,
	apiConversion *apisv1alpha1.APIConversion,
	objectCELTransformationsTimeout time.Duration,
) (*Converter, error) {
	structuralSchemas := make(map[string]*structuralschema.Structural)

	// Gather all the structural schemas for easy map-based indexing
	for _, v := range crd.Spec.Versions {
		if v.Schema == nil {
			continue
		}

		internalJSONSchemaProps := &apiextensionsinternal.JSONSchemaProps{}
		if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(v.Schema.OpenAPIV3Schema, internalJSONSchemaProps, nil); err != nil {
			return nil, fmt.Errorf("failed converting version %s validation to internal version: %w", v.Name, err)
		}

		structuralSchema, err := structuralschema.NewStructural(internalJSONSchemaProps)
		if err != nil {
			return nil, fmt.Errorf("error getting structural schema for version %s: %w", v.Name, err)
		}

		if err := structuraldefaulting.PruneDefaults(structuralSchema); err != nil {
			return nil, fmt.Errorf("error pruning defaults for version %s: %w", v.Name, err)
		}

		structuralSchemas[v.Name] = structuralSchema
	}

	compiledRules, err := Compile(apiConversion, structuralSchemas)
	if err != nil {
		return nil, fmt.Errorf("error compiling conversion rules: %w", err)
	}

	return &Converter{
		apiConversion:                   apiConversion,
		objectCELTransformationsTimeout: objectCELTransformationsTimeout,

		compiledRules: compiledRules,
	}, nil
}

type Converter struct {
	apiConversion                   *apisv1alpha1.APIConversion
	objectCELTransformationsTimeout time.Duration

	compiledRules map[string][]*CompiledRule
}

func (c *Converter) Convert(list *unstructured.UnstructuredList, targetGV schema.GroupVersion) (*unstructured.UnstructuredList, error) {
	// It would be ideal to have ctx passed in, but that is a major change to Kubernetes.
	ctx := context.TODO()

	convertedList := &unstructured.UnstructuredList{}
	for i := range list.Items {
		original := &list.Items[i]

		converted, err := c.convert(ctx, original, targetGV)
		if err != nil {
			return nil, err
		}

		convertedList.Items = append(convertedList.Items, *converted)
	}

	return convertedList, nil
}

func (c *Converter) convert(ctx context.Context, original *unstructured.Unstructured, targetGV schema.GroupVersion) (*unstructured.Unstructured, error) {
	converted := original.DeepCopy()
	converted.SetAPIVersion(targetGV.String())

	originalVersion := original.GetObjectKind().GroupVersionKind().Version
	compiledRules := c.compiledRules[originalVersion]

	ctx, cancel := context.WithTimeout(ctx, c.objectCELTransformationsTimeout)
	defer cancel()

	start := time.Now()
	for _, rule := range compiledRules {
		value, err := evaluateRule(ctx, original, rule)
		if err != nil {
			return nil, fmt.Errorf("error converting: %w", err)
		}

		if err := unstructured.SetNestedField(converted.Object, value, rule.ToFields...); err != nil {
			return nil, fmt.Errorf("error setting destination field %q: %w", rule.ToPath, err)
		}
	}
	celTransformationDuration.WithLabelValues().Observe(time.Since(start).Seconds())

	annotations := converted.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	preserveAnnotation := annotations[preserveAnnotationKey(targetGV.Version)]
	if preserveAnnotation != "" {
		// If we're going to a version that already has the preserve annotation, restore from it
		m := map[string]interface{}{}
		if err := json.Unmarshal([]byte(preserveAnnotation), &m); err != nil {
			return nil, fmt.Errorf("error unmarshaling preserve annotation: %v", err)
		}
		if err := restoreFromPreserveMap(m, converted.Object); err != nil {
			return nil, fmt.Errorf("error processing preserve map: %w", err)
		}

		// Don't save the preserve annotation
		delete(annotations, preserveAnnotationKey(targetGV.Version))
		if len(annotations) == 0 {
			converted.SetAnnotations(nil)
		} else {
			converted.SetAnnotations(annotations)
		}
	} else {
		// Otherwise, store it
		m, err := generatePreserveMap(original, c.apiConversion.Spec.Conversions)
		if err != nil {
			return nil, fmt.Errorf("error preserving fields: %w", err)
		}

		if len(m) > 0 {
			encoded, err := json.Marshal(m)
			if err != nil {
				return nil, fmt.Errorf("error encoding preserve map: %v", err)
			} else {
				annotations[preserveAnnotationKey(originalVersion)] = string(encoded)
				converted.SetAnnotations(annotations)
			}
		}
	}

	return converted, nil
}

func preserveAnnotationKey(version string) string {
	return apisv1alpha1.VersionPreservationAnnotationKeyPrefix + version
}

func evaluateRule(ctx context.Context, original *unstructured.Unstructured, rule *CompiledRule) (interface{}, error) {
	fromValue, exists, err := unstructured.NestedFieldNoCopy(original.Object, rule.FromFields...)
	if err != nil {
		return nil, fmt.Errorf("error getting source field %q: %w", rule.FromPath, err)
	}
	if !exists {
		return nil, nil
	}

	if rule.Program == nil {
		return fromValue, nil
	}

	bindings := map[string]interface{}{
		"self": fromValue,
	}
	activation, err := interpreter.NewActivation(bindings)
	if err != nil {
		return nil, fmt.Errorf("error creating CEL interpreter activation: %w", err)
	}

	evalResult, _, err := rule.Program.ContextEval(ctx, activation)
	if err != nil {
		return nil, fmt.Errorf("error executing transformation: %w", err)
	}

	return evalResult.Value(), nil
}

func generatePreserveMap(original *unstructured.Unstructured, conversions []apisv1alpha1.APIVersionConversion) (map[string]interface{}, error) {
	m := map[string]interface{}{}
	originalVersion := original.GetObjectKind().GroupVersionKind().Version

	for _, conversion := range conversions {
		if conversion.From != originalVersion {
			continue
		}
		for _, field := range conversion.Preserve {
			if field[0] == '.' {
				field = field[1:]
			}
			v, exists, err := unstructured.NestedFieldNoCopy(original.Object, strings.Split(field, ".")...)
			if err != nil {
				return nil, fmt.Errorf("error getting field %q to preserve: %v", field, err)
			}
			if !exists {
				continue
			}
			m[field] = v
		}
	}

	return m, nil
}

func restoreFromPreserveMap(m map[string]interface{}, obj map[string]interface{}) error {
	for field, val := range m {
		if err := unstructured.SetNestedField(obj, val, strings.Split(field, ".")...); err != nil {
			return fmt.Errorf("error setting nested field %q, value %q: %v", field, val, err)
		}
	}

	return nil
}
