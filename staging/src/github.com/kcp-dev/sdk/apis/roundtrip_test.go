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

package apis

import (
	"flag"
	"math/rand"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/randfill"

	"k8s.io/apimachinery/pkg/api/apitesting/fuzzer"
	"k8s.io/apimachinery/pkg/api/apitesting/roundtrip"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubeconversion "k8s.io/apimachinery/pkg/conversion"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	sdktesting "github.com/kcp-dev/sdk/testing"
)

// in non-race-detection mode, a higher number of iterations is reasonable.
const defaultFuzzIters = 200

var FuzzIters = flag.Int("fuzz-iters", defaultFuzzIters, "How many fuzzing iterations to do.")

var groups = []runtime.SchemeBuilder{
	apisv1alpha2.SchemeBuilder,
	apisv1alpha1.SchemeBuilder,
}

// external -> json/protobuf -> external.
func TestRoundTripTypes(t *testing.T) {
	scheme := runtime.NewScheme()
	codecs := serializer.NewCodecFactory(scheme)
	for _, builder := range groups {
		require.NoError(t, builder.AddToScheme(scheme))
	}
	seed := rand.Int63()
	fuzzer := fuzzer.FuzzerFor(sdktesting.FuzzerFuncs, rand.NewSource(seed), codecs)
	roundtrip.RoundTripExternalTypesWithoutProtobuf(t, scheme, codecs, fuzzer, nil)
}

type ConversionTests struct {
	name                  string
	v1                    runtime.Object
	v2                    runtime.Object
	toV2                  func(in, out runtime.Object, s kubeconversion.Scope) error
	toV1                  func(in, out runtime.Object, s kubeconversion.Scope) error
	v1ExpectedAnnotations []string
	v2ExpectedAnnotations []string
}

func TestConversion(t *testing.T) {
	tests := []ConversionTests{
		{
			name: "APIExport",
			v1:   &apisv1alpha1.APIExport{},
			v2:   &apisv1alpha2.APIExport{},
			toV2: func(in, out runtime.Object, s kubeconversion.Scope) error {
				return apisv1alpha2.Convert_v1alpha1_APIExport_To_v1alpha2_APIExport(in.(*apisv1alpha1.APIExport), out.(*apisv1alpha2.APIExport), s)
			},
			toV1: func(in, out runtime.Object, s kubeconversion.Scope) error {
				return apisv1alpha2.Convert_v1alpha2_APIExport_To_v1alpha1_APIExport(in.(*apisv1alpha2.APIExport), out.(*apisv1alpha1.APIExport), s)
			},
			v2ExpectedAnnotations: []string{
				apisv1alpha2.PermissionClaimsV1Alpha1Annotation,
			},
		},
		{
			name: "APIExportList",
			v1:   &apisv1alpha1.APIExportList{},
			v2:   &apisv1alpha2.APIExportList{},
			toV2: func(in, out runtime.Object, s kubeconversion.Scope) error {
				return apisv1alpha2.Convert_v1alpha1_APIExportList_To_v1alpha2_APIExportList(in.(*apisv1alpha1.APIExportList), out.(*apisv1alpha2.APIExportList), s)
			},
			toV1: func(in, out runtime.Object, s kubeconversion.Scope) error {
				return apisv1alpha2.Convert_v1alpha2_APIExportList_To_v1alpha1_APIExportList(in.(*apisv1alpha2.APIExportList), out.(*apisv1alpha1.APIExportList), s)
			},
			v2ExpectedAnnotations: []string{
				apisv1alpha2.PermissionClaimsV1Alpha1Annotation,
			},
		},
		{
			name: "APIBinding",
			v1:   &apisv1alpha1.APIBinding{},
			v2:   &apisv1alpha2.APIBinding{},
			toV2: func(in, out runtime.Object, s kubeconversion.Scope) error {
				return apisv1alpha2.Convert_v1alpha1_APIBinding_To_v1alpha2_APIBinding(in.(*apisv1alpha1.APIBinding), out.(*apisv1alpha2.APIBinding), s)
			},
			toV1: func(in, out runtime.Object, s kubeconversion.Scope) error {
				return apisv1alpha2.Convert_v1alpha2_APIBinding_To_v1alpha1_APIBinding(in.(*apisv1alpha2.APIBinding), out.(*apisv1alpha1.APIBinding), s)
			},
			v2ExpectedAnnotations: []string{
				apisv1alpha2.StatusPermissionClaimsV1Alpha1Annotation,
			},
		},
		{
			name: "APIBindingList",
			v1:   &apisv1alpha1.APIBindingList{},
			v2:   &apisv1alpha2.APIBindingList{},
			toV2: func(in, out runtime.Object, s kubeconversion.Scope) error {
				return apisv1alpha2.Convert_v1alpha1_APIBindingList_To_v1alpha2_APIBindingList(in.(*apisv1alpha1.APIBindingList), out.(*apisv1alpha2.APIBindingList), s)
			},
			toV1: func(in, out runtime.Object, s kubeconversion.Scope) error {
				return apisv1alpha2.Convert_v1alpha2_APIBindingList_To_v1alpha1_APIBindingList(in.(*apisv1alpha2.APIBindingList), out.(*apisv1alpha1.APIBindingList), s)
			},
			v2ExpectedAnnotations: []string{
				apisv1alpha2.StatusPermissionClaimsV1Alpha1Annotation,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for range *FuzzIters {
				testGenericConversion(t, test.v1.DeepCopyObject, test.v2.DeepCopyObject, test.toV1, test.toV2, test.v1ExpectedAnnotations, test.v2ExpectedAnnotations)
			}
		})
	}
}

func testGenericConversion(
	t *testing.T,
	v1Factory func() runtime.Object,
	v2Factory func() runtime.Object,
	toV1 func(in, out runtime.Object, s kubeconversion.Scope) error,
	toV2 func(in, out runtime.Object, s kubeconversion.Scope) error,
	v1ExpectedAnnotations, v2ExpectedAnnotations []string,
) {
	scheme := runtime.NewScheme()
	codecs := serializer.NewCodecFactory(scheme)
	for _, builder := range groups {
		require.NoError(t, builder.AddToScheme(scheme))
	}
	seed := rand.Int63()
	fuzzer := fuzzer.FuzzerFor(sdktesting.FuzzerFuncs, rand.NewSource(seed), codecs)

	t.Run("V1->V2->V1", func(t *testing.T) {
		original := v1Factory()
		result := v1Factory()
		fuzzInternalObject(t, fuzzer, original)
		originalCopy := original.DeepCopyObject()

		intermediate := v2Factory()

		err := toV2(original, intermediate, nil)
		require.NoError(t, err)

		err = toV1(intermediate, result, nil)
		require.NoError(t, err)

		// Remove annotations that we expect on the object due to conversion data stored.
		removeAnnotations(t, result, v1ExpectedAnnotations)

		require.True(t, apiequality.Semantic.DeepEqual(original, result), "expected original to equal result, got diff: %v", cmp.Diff(original, result))
		require.True(t, apiequality.Semantic.DeepEqual(originalCopy, original), "expected originalCopy to equal original, got diff: %v", cmp.Diff(originalCopy, original))
	})

	t.Run("V2->V1->V2", func(t *testing.T) {
		original := v2Factory()
		result := v2Factory()
		fuzzInternalObject(t, fuzzer, original)
		originalCopy := original.DeepCopyObject()

		intermediate := v1Factory()

		err := toV1(original, intermediate, nil)
		require.NoError(t, err)

		err = toV2(intermediate, result, nil)
		require.NoError(t, err)

		// Remove annotations that we expected on the converted object due to conversion data stored.
		// To do a equality check with the original object, we need to remove those annotations as we
		// know that they will be there.
		removeAnnotations(t, result, v2ExpectedAnnotations)

		require.True(t, apiequality.Semantic.DeepEqual(original, result), "expected original to equal result, got diff: %v", cmp.Diff(original, result))
		require.True(t, apiequality.Semantic.DeepEqual(originalCopy, original), "expected originalCopy to equal original, got diff: %v", cmp.Diff(originalCopy, original))
	})
}

func removeAnnotations(t *testing.T, object runtime.Object, annotationsToRemove []string) {
	if len(annotationsToRemove) == 0 {
		// nothing to remove, exit early. no reason to do all the conversion below.
		return
	}

	resultMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		t.Fatalf("Failed converting to unstructured: %v for %#v", err, object)
	}
	u := unstructured.Unstructured{Object: resultMap}

	if u.IsList() {
		if err := u.EachListItem(func(obj runtime.Object) error {
			objMeta := obj.(metav1.Object)
			annotations := objMeta.GetAnnotations()
			if annotations != nil {
				for _, annotation := range annotationsToRemove {
					delete(annotations, annotation)
				}
			}
			objMeta.SetAnnotations(annotations)
			return nil
		}); err != nil {
			t.Fatalf("Failed iterating over list items: %v", err)
		}
	} else {
		annotations := u.GetAnnotations()
		if annotations != nil {
			for _, annotation := range annotationsToRemove {
				delete(annotations, annotation)
			}
		}
		u.SetAnnotations(annotations)
	}

	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, object); err != nil {
		t.Fatalf("Failed converting from unstructured: %v for %#v", err, object)
	}
}

// fuzzInternalObject fuzzes an arbitrary runtime object using the appropriate
// fuzzer registered with the apitesting package.
func fuzzInternalObject(t *testing.T, fuzzer *randfill.Filler, object runtime.Object) runtime.Object {
	fuzzer.Fill(object)

	j, err := apimeta.TypeAccessor(object)
	if err != nil {
		t.Fatalf("Unexpected error %v for %#v", err, object)
	}
	j.SetKind("")
	j.SetAPIVersion("")

	return object
}
