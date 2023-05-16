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

package pathannotation

import (
	"context"
	"fmt"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/endpoints/request"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

func TestPathAnnotationAdmit(t *testing.T) {
	scenarios := []struct {
		name              string
		admissionObject   runtime.Object
		admissionResource schema.GroupVersionResource
		admissionVerb     admission.Operation
		admissionOptions  runtime.Object
		admissionContext  context.Context //nolint:containedctx
		getLogicalCluster func(clusterName logicalcluster.Name, name string) (*corev1alpha1.LogicalCluster, error)

		expectError             bool
		validateAdmissionObject func(t *testing.T, obj runtime.Object)
	}{
		{
			name:             "error when no cluster in the context",
			admissionContext: context.TODO(),
			expectError:      true,
		},
		{
			name:                    "admission is not applied to logicalclusters",
			admissionContext:        admissionContextFor("foo"),
			admissionResource:       corev1alpha1.SchemeGroupVersion.WithResource("logicalclusters"),
			admissionObject:         &corev1alpha1.LogicalCluster{},
			validateAdmissionObject: objectWithoutPathAnnotation,
		},
		{
			name:                    "admission is not applied to a resource that undergoes a deletion",
			admissionContext:        admissionContextFor("foo"),
			admissionResource:       apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"),
			admissionVerb:           admission.Delete,
			admissionObject:         &apisv1alpha1.APIExport{},
			validateAdmissionObject: objectWithoutPathAnnotation,
		},
		{
			name:              "admission is not applied to an unsupported resource",
			admissionContext:  admissionContextFor("foo"),
			admissionResource: apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas"),
			admissionVerb:     admission.Create,
			admissionObject:   &apisv1alpha1.APIResourceSchema{},
			getLogicalCluster: getCluster("foo"),
			validateAdmissionObject: func(t *testing.T, obj runtime.Object) {
				t.Helper()
				objMeta, err := meta.Accessor(obj)
				if err != nil {
					t.Fatal(err)
				}
				if _, has := objMeta.GetAnnotations()[core.LogicalClusterPathAnnotationKey]; has {
					t.Fatalf("the %q annotation cannot be automatically set on an APIResourceSchema resource", core.LogicalClusterPathAnnotationKey)
				}
			},
		},
		{
			name:                    "admission is applied to an unsupported resource if it has the path annotation present",
			admissionContext:        admissionContextFor("foo"),
			admissionResource:       apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas"),
			admissionVerb:           admission.Create,
			admissionObject:         &apisv1alpha1.APIResourceSchema{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{core.LogicalClusterPathAnnotationKey: ""}}},
			getLogicalCluster:       getCluster("foo"),
			validateAdmissionObject: objectHasPathAnnotation("root:foo"),
		},
		{
			name:                    "a path is derived from the LogicalCluster object if it doesn't have the path annotation",
			admissionContext:        admissionContextFor("foo"),
			admissionVerb:           admission.Create,
			admissionResource:       apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"),
			admissionObject:         &apisv1alpha1.APIExport{},
			getLogicalCluster:       getCluster("foo"),
			validateAdmissionObject: objectHasPathAnnotation("root:foo"),
		},
		{
			name:                    "a path is updated when is different from the one applied to the LogicalCluster resource",
			admissionVerb:           admission.Create,
			admissionResource:       apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"),
			admissionObject:         &apisv1alpha1.APIExport{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{core.LogicalClusterPathAnnotationKey: "bar:foo"}}},
			admissionContext:        admissionContextFor("foo"),
			getLogicalCluster:       getCluster("foo"),
			validateAdmissionObject: objectHasPathAnnotation("root:foo"),
		},
		{
			name:                    "happy path: an APIExport is annotated with a path",
			admissionVerb:           admission.Create,
			admissionResource:       apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"),
			admissionObject:         &apisv1alpha1.APIExport{},
			admissionContext:        admissionContextFor("foo"),
			getLogicalCluster:       getCluster("foo"),
			validateAdmissionObject: objectHasPathAnnotation("root:foo"),
		},

		{
			name:                    "happy path: a WorkspaceType is annotated with a path",
			admissionVerb:           admission.Create,
			admissionResource:       tenancyv1alpha1.SchemeGroupVersion.WithResource("workspacetypes"),
			admissionObject:         &tenancyv1alpha1.WorkspaceType{},
			admissionContext:        admissionContextFor("foo"),
			getLogicalCluster:       getCluster("foo"),
			validateAdmissionObject: objectHasPathAnnotation("root:foo"),
		},

		{
			name:                    "happy path: a WorkspaceType is annotated with a path",
			admissionVerb:           admission.Create,
			admissionResource:       tenancyv1alpha1.SchemeGroupVersion.WithResource("workspacetypes"),
			admissionObject:         &tenancyv1alpha1.WorkspaceType{},
			admissionContext:        admissionContextFor("foo"),
			getLogicalCluster:       getCluster("foo"),
			validateAdmissionObject: objectHasPathAnnotation("root:foo"),
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			target := &pathAnnotationPlugin{getLogicalCluster: scenario.getLogicalCluster}
			attr := admission.NewAttributesRecord(
				scenario.admissionObject,
				nil,
				schema.GroupVersionKind{},
				"",
				"",
				scenario.admissionResource,
				"",
				scenario.admissionVerb,
				scenario.admissionOptions,
				false,
				nil,
			)

			err := target.Admit(scenario.admissionContext, attr, nil)

			if scenario.expectError && err == nil {
				t.Errorf("expected to get an error")
			}
			if !scenario.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if scenario.validateAdmissionObject != nil {
				scenario.validateAdmissionObject(t, scenario.admissionObject)
			}
		})
	}
}

func TestPathAnnotationValidate(t *testing.T) {
	scenarios := []struct {
		name              string
		admissionObject   runtime.Object
		admissionResource schema.GroupVersionResource
		admissionVerb     admission.Operation
		admissionOptions  runtime.Object
		admissionContext  context.Context //nolint:containedctx
		getLogicalCluster func(clusterName logicalcluster.Name, name string) (*corev1alpha1.LogicalCluster, error)

		expectError bool
	}{
		{
			name:             "error when no cluster in the context",
			admissionContext: context.TODO(),
			expectError:      true,
		},
		{
			name:              "admission is not applied to logicalclusters",
			admissionContext:  admissionContextFor("foo"),
			admissionResource: corev1alpha1.SchemeGroupVersion.WithResource("logicalclusters"),
		},
		{
			name:              "admission is not applied to a resource that undergoes a deletion",
			admissionContext:  admissionContextFor("foo"),
			admissionResource: apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"),
			admissionVerb:     admission.Delete,
		},
		{
			name:              "admission is not applied to an unsupported resource",
			admissionContext:  admissionContextFor("foo"),
			admissionResource: apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas"),
			admissionVerb:     admission.Create,
			admissionObject:   &apisv1alpha1.APIResourceSchema{},
			getLogicalCluster: getCluster("foo"),
		},
		{
			name:              "an APIExport with incorrect path annotation is NOT admitted",
			admissionVerb:     admission.Create,
			admissionResource: apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"),
			admissionObject:   &apisv1alpha1.APIExport{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{core.LogicalClusterPathAnnotationKey: "universe:milky-way"}}},
			admissionContext:  admissionContextFor("foo"),
			getLogicalCluster: getCluster("foo"),
			expectError:       true,
		},
		{
			name:              "an APIExport without the path annotation is NOT admitted",
			admissionVerb:     admission.Create,
			admissionResource: apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"),
			admissionObject:   &apisv1alpha1.APIExport{},
			admissionContext:  admissionContextFor("foo"),
			getLogicalCluster: getCluster("foo"),
			expectError:       true,
		},
		{
			name:              "happy path: an APIExport with the path annotation is admitted",
			admissionVerb:     admission.Create,
			admissionResource: apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"),
			admissionObject:   &apisv1alpha1.APIExport{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{core.LogicalClusterPathAnnotationKey: "root:foo"}}},
			admissionContext:  admissionContextFor("foo"),
			getLogicalCluster: getCluster("foo"),
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			target := &pathAnnotationPlugin{getLogicalCluster: scenario.getLogicalCluster}
			attr := admission.NewAttributesRecord(
				scenario.admissionObject,
				nil,
				schema.GroupVersionKind{},
				"",
				"",
				scenario.admissionResource,
				"",
				scenario.admissionVerb,
				scenario.admissionOptions,
				false,
				nil,
			)

			err := target.Validate(scenario.admissionContext, attr, nil)

			if scenario.expectError && err == nil {
				t.Errorf("expected to get an error")
			}
			if !scenario.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func getCluster(expectedClusterName string) func(clusterName logicalcluster.Name, name string) (*corev1alpha1.LogicalCluster, error) {
	return func(clusterName logicalcluster.Name, name string) (*corev1alpha1.LogicalCluster, error) {
		if clusterName.String() != expectedClusterName {
			return nil, fmt.Errorf("unexpected clusterName = %q, expected = %q", clusterName, expectedClusterName)
		}
		if name != corev1alpha1.LogicalClusterName {
			return nil, fmt.Errorf("unexpected name = %q, expected = %q", clusterName, corev1alpha1.LogicalClusterName)
		}
		return &corev1alpha1.LogicalCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: corev1alpha1.LogicalClusterName,
				Annotations: map[string]string{
					core.LogicalClusterPathAnnotationKey: "root:foo",
				},
			},
		}, nil
	}
}

func objectHasPathAnnotation(expectedPathAnnotation string) func(t *testing.T, obj runtime.Object) {
	return func(t *testing.T, obj runtime.Object) {
		t.Helper()
		objMeta, err := meta.Accessor(obj)
		if err != nil {
			t.Fatal(err)
		}
		pathAnnotation := objMeta.GetAnnotations()[core.LogicalClusterPathAnnotationKey]
		if pathAnnotation == "" || pathAnnotation != expectedPathAnnotation {
			t.Fatalf("unexpected value = %q, in the %q annotation, expected = %q", pathAnnotation, core.LogicalClusterPathAnnotationKey, expectedPathAnnotation)
		}
	}
}

func objectWithoutPathAnnotation(t *testing.T, obj runtime.Object) {
	t.Helper()
	objMeta, err := meta.Accessor(obj)
	if err != nil {
		t.Fatal(err)
	}
	_, has := objMeta.GetAnnotations()[core.LogicalClusterPathAnnotationKey]
	if has {
		t.Fatalf("object = %v should not have %q annotation set", objMeta.GetName(), core.LogicalClusterPathAnnotationKey)
	}
}

func admissionContextFor(clusterName string) context.Context {
	return request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.Name(clusterName)})
}
