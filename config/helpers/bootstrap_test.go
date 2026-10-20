/*
Copyright 2026 The kcp Authors.

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

package helpers

import (
	"bytes"
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

var apiExportGVR = schema.GroupVersionResource{Group: "apis.kcp.io", Version: "v1alpha2", Resource: "apiexports"}

func apiExport(identity map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apis.kcp.io/v1alpha2",
		"kind":       "APIExport",
		"metadata":   map[string]interface{}{"name": "tenancy.kcp.io"},
		"spec":       map[string]interface{}{"maximalPermissionPolicy": map[string]interface{}{"local": map[string]interface{}{}}},
	}}
	if identity != nil {
		_ = unstructured.SetNestedField(u.Object, identity, "spec", "identity")
	}
	return u
}

func rotatedIdentity() map[string]interface{} {
	return map[string]interface{}{"secretRef": map[string]interface{}{"namespace": "kcp-system", "name": "tenancy.kcp.io-v2"}}
}

func TestCreateResourceKeepsAPIExportIdentity(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{apiExportGVR: "APIExportList"},
		apiExport(rotatedIdentity()),
	)
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(schema.GroupVersionKind{Group: "apis.kcp.io", Version: "v1alpha2", Kind: "APIExport"}, meta.RESTScopeRoot)

	// the bootstrap manifest does not set spec.identity.
	if err := createResource(context.Background(), client, mapper, apiExport(nil)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := client.Resource(apiExportGVR).Get(context.Background(), "tenancy.kcp.io", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	name, _, _ := unstructured.NestedString(got.Object, "spec", "identity", "secretRef", "name")
	if name != "tenancy.kcp.io-v2" {
		t.Fatalf("expected bootstrap to keep spec.identity.secretRef.name tenancy.kcp.io-v2, got %q", name)
	}
	if _, found, _ := unstructured.NestedMap(got.Object, "spec", "maximalPermissionPolicy"); !found {
		t.Fatal("expected the rest of the spec to be updated from the manifest")
	}
}

func TestPreserveAPIExportIdentity(t *testing.T) {
	t.Parallel()

	manifestIdentity := map[string]interface{}{"secretRef": map[string]interface{}{"namespace": "kcp-system", "name": "from-manifest"}}
	other := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]interface{}{"name": "cm"},
	}}

	scenarios := []struct {
		name     string
		existing *unstructured.Unstructured
		desired  *unstructured.Unstructured
		wantName string
	}{
		{name: "identity is carried over when the manifest has none", existing: apiExport(rotatedIdentity()), desired: apiExport(nil), wantName: "tenancy.kcp.io-v2"},
		{name: "an identity set by the manifest wins", existing: apiExport(rotatedIdentity()), desired: apiExport(manifestIdentity), wantName: "from-manifest"},
		{name: "nothing to carry over", existing: apiExport(nil), desired: apiExport(nil)},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			before := scenario.desired.DeepCopy()
			got, err := preserveAPIExportIdentity(scenario.existing, scenario.desired)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			name, _, _ := unstructured.NestedString(got.Object, "spec", "identity", "secretRef", "name")
			if name != scenario.wantName {
				t.Fatalf("expected secretRef name %q, got %q", scenario.wantName, name)
			}
			if !equalObjects(before, scenario.desired) {
				t.Fatal("the desired object passed in must not be mutated")
			}
		})
	}

	t.Run("other kinds are not touched", func(t *testing.T) {
		t.Parallel()
		got, err := preserveAPIExportIdentity(apiExport(rotatedIdentity()), other)
		if err != nil || got != other {
			t.Fatalf("expected the object to be returned unchanged, got %v, %v", got, err)
		}
	})
}

func equalObjects(a, b *unstructured.Unstructured) bool {
	ja, _ := a.MarshalJSON()
	jb, _ := b.MarshalJSON()
	return bytes.Equal(ja, jb)
}
