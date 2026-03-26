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

package main

import (
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newCRD(versions ...string) *apiextensionsv1.CustomResourceDefinition {
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "test.example.com"},
	}
	for _, v := range versions {
		crd.Spec.Versions = append(crd.Spec.Versions, apiextensionsv1.CustomResourceDefinitionVersion{
			Name: v,
			Schema: &apiextensionsv1.CustomResourceValidation{
				OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
					Type:        "object",
					Description: "schema for " + v,
				},
			},
		})
	}
	return crd
}

func TestResolveVersion_EmptyNameReturnsFirst(t *testing.T) {
	crd := newCRD("v1", "v2")

	ver, err := resolveVersion(crd, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ver.Name != "v1" {
		t.Errorf("expected version v1, got %s", ver.Name)
	}
}

func TestResolveVersion_MatchByName(t *testing.T) {
	crd := newCRD("v1alpha1", "v1", "v2")

	for _, name := range []string{"v1alpha1", "v1", "v2"} {
		ver, err := resolveVersion(crd, name)
		if err != nil {
			t.Fatalf("unexpected error for %s: %v", name, err)
		}
		if ver.Name != name {
			t.Errorf("expected version %s, got %s", name, ver.Name)
		}
		if ver.Schema.OpenAPIV3Schema.Description != "schema for "+name {
			t.Errorf("got wrong schema for %s", name)
		}
	}
}

func TestResolveVersion_NotFound(t *testing.T) {
	crd := newCRD("v1", "v2")

	_, err := resolveVersion(crd, "v3")
	if err == nil {
		t.Fatal("expected error for missing version, got nil")
	}
}

func TestResolveVersion_ReturnsSamePointer(t *testing.T) {
	crd := newCRD("v1", "v2")

	ver, err := resolveVersion(crd, "v2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ver != &crd.Spec.Versions[1] {
		t.Error("expected pointer into original Spec.Versions slice")
	}
}
