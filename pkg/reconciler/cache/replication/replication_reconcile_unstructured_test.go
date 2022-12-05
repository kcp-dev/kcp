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

package replication

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v3"

	kcpfakedynamic "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/dynamic/fake"
	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func TestEnsureUnstructuredSpec(t *testing.T) {
	scenarios := []struct {
		name                    string
		cacheObject             *unstructured.Unstructured
		localObject             *unstructured.Unstructured
		expectSpecChanged       bool
		expectError             bool
		validateCacheObjectSpec func(ts *testing.T, cacheObject, localObject *unstructured.Unstructured)
	}{
		{
			name:        "no-op: empty",
			cacheObject: &unstructured.Unstructured{},
			localObject: &unstructured.Unstructured{},
		},
		{
			name:        "local has the spec but cached hasn't",
			cacheObject: &unstructured.Unstructured{Object: map[string]interface{}{}},
			localObject: &unstructured.Unstructured{Object: map[string]interface{}{
				"spec": map[string]interface{}{},
			}},
			expectSpecChanged: true,
			validateCacheObjectSpec: func(ts *testing.T, cacheObject, localObject *unstructured.Unstructured) {
				if _, hasSpec := cacheObject.Object["spec"]; !hasSpec {
					ts.Fatal("the cachedObject doesn't have the spec field")
				}
				if _, hasSpec := localObject.Object["spec"]; !hasSpec {
					ts.Fatal("the localObject was modified and doesn't have the spec field anymore")
				}
			},
		},
		{
			name: "cache has the spec but local hasn't",
			cacheObject: &unstructured.Unstructured{Object: map[string]interface{}{
				"spec": map[string]interface{}{},
			}},
			localObject:       &unstructured.Unstructured{Object: map[string]interface{}{}},
			expectSpecChanged: true,
			validateCacheObjectSpec: func(ts *testing.T, cacheObject, localObject *unstructured.Unstructured) {
				if _, hasSpec := cacheObject.Object["spec"]; hasSpec {
					ts.Fatal("the cacheObject has the spec field")
				}
				if _, hasSpec := localObject.Object["spec"]; hasSpec {
					ts.Fatal("the localObject has the spec field")
				}
			},
		},
		{
			name: "local different than cache",
			cacheObject: &unstructured.Unstructured{Object: map[string]interface{}{
				"spec": map[string]interface{}{"fieldA": "a"},
			}},
			localObject: &unstructured.Unstructured{Object: map[string]interface{}{
				"spec": map[string]interface{}{"fieldA": "a", "fieldB": "b"},
			}},
			expectSpecChanged: true,
			validateCacheObjectSpec: func(ts *testing.T, cacheObject, localObject *unstructured.Unstructured) {
				expectedObject := &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"fieldA": "a", "fieldB": "b"},
				}}
				if !reflect.DeepEqual(cacheObject.Object, expectedObject.Object) {
					ts.Errorf("received spec differs from the expected one :\n%s", cmp.Diff(cacheObject.Object, expectedObject.Object))
				}
			},
		},
		{
			name: "cache different than local",
			cacheObject: &unstructured.Unstructured{Object: map[string]interface{}{
				"spec": map[string]interface{}{"fieldA": "a", "fieldB": "b"},
			}},
			localObject: &unstructured.Unstructured{Object: map[string]interface{}{
				"spec": map[string]interface{}{"fieldA": "a"},
			}},
			expectSpecChanged: true,
			validateCacheObjectSpec: func(ts *testing.T, cacheObject, localObject *unstructured.Unstructured) {
				expectedObject := &unstructured.Unstructured{Object: map[string]interface{}{
					"spec": map[string]interface{}{"fieldA": "a"},
				}}
				if !reflect.DeepEqual(cacheObject.Object, expectedObject.Object) {
					ts.Errorf("received spec differs from the expected one :\n%s", cmp.Diff(cacheObject.Object, expectedObject.Object))
				}
			},
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(tt *testing.T) {
			specChanged, err := ensureRemaining(scenario.cacheObject, scenario.localObject)
			if specChanged != scenario.expectSpecChanged {
				tt.Fatalf("spec changed = %v, expected spec to be changed = %v", specChanged, scenario.expectSpecChanged)
			}
			if scenario.expectError && err == nil {
				tt.Errorf("expected to get an error")
			}
			if !scenario.expectError && err != nil {
				tt.Errorf("unexpected error: %v", err)
			}
			if scenario.validateCacheObjectSpec != nil {
				scenario.validateCacheObjectSpec(tt, scenario.cacheObject, scenario.localObject)
			}
		})
	}
}

func TestEnsureUnstructuredStatus(t *testing.T) {
	scenarios := []struct {
		name                    string
		cacheObject             *unstructured.Unstructured
		localObject             *unstructured.Unstructured
		expectStatusChanged     bool
		expectError             bool
		validateCacheObjectSpec func(ts *testing.T, cacheObject, localObject *unstructured.Unstructured)
	}{
		{
			name:        "no-op: empty",
			cacheObject: &unstructured.Unstructured{},
			localObject: &unstructured.Unstructured{},
		},
		{
			name:        "local has the status but cached hasn't",
			cacheObject: &unstructured.Unstructured{Object: map[string]interface{}{}},
			localObject: &unstructured.Unstructured{Object: map[string]interface{}{
				"status": map[string]interface{}{},
			}},
			expectStatusChanged: true,
			validateCacheObjectSpec: func(ts *testing.T, cacheObject, localObject *unstructured.Unstructured) {
				if _, hasStatus := cacheObject.Object["status"]; !hasStatus {
					ts.Fatal("the cachedObject doesn't have the status field")
				}
				if _, hasStatus := localObject.Object["status"]; !hasStatus {
					ts.Fatal("the localObject was modified and doesn't have the status field anymore")
				}
			},
		},
		{
			name: "cache has the status but local hasn't",
			cacheObject: &unstructured.Unstructured{Object: map[string]interface{}{
				"status": map[string]interface{}{},
			}},
			localObject:         &unstructured.Unstructured{Object: map[string]interface{}{}},
			expectStatusChanged: true,
			validateCacheObjectSpec: func(ts *testing.T, cacheObject, localObject *unstructured.Unstructured) {
				if _, hasStatus := cacheObject.Object["status"]; hasStatus {
					ts.Fatal("the cacheObject has the status field")
				}
				if _, hasStatus := localObject.Object["status"]; hasStatus {
					ts.Fatal("the localObject has the status field")
				}
			},
		},
		{
			name: "local different than cache",
			cacheObject: &unstructured.Unstructured{Object: map[string]interface{}{
				"status": map[string]interface{}{"fieldA": "a"},
			}},
			localObject: &unstructured.Unstructured{Object: map[string]interface{}{
				"status": map[string]interface{}{"fieldA": "a", "fieldB": "b"},
			}},
			expectStatusChanged: true,
			validateCacheObjectSpec: func(ts *testing.T, cacheObject, localObject *unstructured.Unstructured) {
				expectedObject := &unstructured.Unstructured{Object: map[string]interface{}{
					"status": map[string]interface{}{"fieldA": "a", "fieldB": "b"},
				}}
				if !reflect.DeepEqual(cacheObject.Object, expectedObject.Object) {
					ts.Errorf("received status differs from the expected one :\n%s", cmp.Diff(cacheObject.Object, expectedObject.Object))
				}
			},
		},
		{
			name: "cache different than local",
			cacheObject: &unstructured.Unstructured{Object: map[string]interface{}{
				"status": map[string]interface{}{"fieldA": "a", "fieldB": "b"},
			}},
			localObject: &unstructured.Unstructured{Object: map[string]interface{}{
				"status": map[string]interface{}{"fieldA": "a"},
			}},
			expectStatusChanged: true,
			validateCacheObjectSpec: func(ts *testing.T, cacheObject, localObject *unstructured.Unstructured) {
				expectedObject := &unstructured.Unstructured{Object: map[string]interface{}{
					"status": map[string]interface{}{"fieldA": "a"},
				}}
				if !reflect.DeepEqual(cacheObject.Object, expectedObject.Object) {
					ts.Errorf("received status differs from the expected one :\n%s", cmp.Diff(cacheObject.Object, expectedObject.Object))
				}
			},
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(tt *testing.T) {
			statusChanged, err := ensureRemaining(scenario.cacheObject, scenario.localObject)
			if statusChanged != scenario.expectStatusChanged {
				tt.Fatalf("status changed = %v, expected spec to be changed = %v", statusChanged, scenario.expectStatusChanged)
			}
			if scenario.expectError && err == nil {
				tt.Errorf("expected to get an error")
			}
			if !scenario.expectError && err != nil {
				tt.Errorf("unexpected error: %v", err)
			}
			if scenario.validateCacheObjectSpec != nil {
				scenario.validateCacheObjectSpec(tt, scenario.cacheObject, scenario.localObject)
			}
		})
	}
}

func TestEnsureUnstructuredMeta(t *testing.T) {
	scenarios := []struct {
		name                    string
		cacheObjectMeta         metav1.ObjectMeta
		localObjectMeta         metav1.ObjectMeta
		validateCacheObjectMeta func(ts *testing.T, cacheObjectMeta, localObjectMeta metav1.ObjectMeta)
		expectObjectMetaChanged bool
	}{
		{
			name: "no-op: empty",
		},
		{
			name:            "no-op: equal",
			cacheObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"a": "b"}},
			localObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"a": "b"}},
		},
		{
			name:            "no-op: equal with rv",
			cacheObjectMeta: metav1.ObjectMeta{ResourceVersion: "1", Annotations: map[string]string{"a": "b"}},
			localObjectMeta: metav1.ObjectMeta{ResourceVersion: "2", Annotations: map[string]string{"a": "b"}},
			validateCacheObjectMeta: func(ts *testing.T, cacheObjectMeta, localObjectMeta metav1.ObjectMeta) {
				expectedCacheObjectMeta := metav1.ObjectMeta{ResourceVersion: "1", Annotations: map[string]string{"a": "b"}}
				if !reflect.DeepEqual(cacheObjectMeta, expectedCacheObjectMeta) {
					ts.Errorf("received metadata differs from the expected one :\n%s", cmp.Diff(cacheObjectMeta, expectedCacheObjectMeta))
				}

				expectedLocalObjectMeta := metav1.ObjectMeta{ResourceVersion: "2", Annotations: map[string]string{"a": "b"}}
				if !reflect.DeepEqual(localObjectMeta, expectedLocalObjectMeta) {
					ts.Errorf("local object's metadata mustn't be changed, diff :\n%s", cmp.Diff(localObjectMeta, expectedLocalObjectMeta))
				}
			},
		},
		{
			name:                    "annotations on local different",
			cacheObjectMeta:         metav1.ObjectMeta{ResourceVersion: "1", Annotations: map[string]string{"a": "b"}},
			localObjectMeta:         metav1.ObjectMeta{ResourceVersion: "2", Annotations: map[string]string{"a": "b", "foo": "bar"}},
			expectObjectMetaChanged: true,
			validateCacheObjectMeta: func(ts *testing.T, cacheObjectMeta, localObjectMeta metav1.ObjectMeta) {
				expectedCacheObjectMeta := metav1.ObjectMeta{ResourceVersion: "1", Annotations: map[string]string{"a": "b", "foo": "bar"}}
				if !reflect.DeepEqual(cacheObjectMeta, expectedCacheObjectMeta) {
					ts.Errorf("received metadata differs from the expected one :\n%s", cmp.Diff(cacheObjectMeta, expectedCacheObjectMeta))
				}

				expectedLocalObjectMeta := metav1.ObjectMeta{ResourceVersion: "2", Annotations: map[string]string{"a": "b", "foo": "bar"}}
				if !reflect.DeepEqual(localObjectMeta, expectedLocalObjectMeta) {
					ts.Errorf("local object's metadata mustn't be changed, diff :\n%s", cmp.Diff(localObjectMeta, expectedLocalObjectMeta))
				}
			},
		},
		{
			name:                    "annotations on cached different",
			cacheObjectMeta:         metav1.ObjectMeta{ResourceVersion: "1", Annotations: map[string]string{"a": "b", "foo": "bar"}},
			localObjectMeta:         metav1.ObjectMeta{ResourceVersion: "2", Annotations: map[string]string{"a": "b"}},
			expectObjectMetaChanged: true,
			validateCacheObjectMeta: func(ts *testing.T, cacheObjectMeta, localObjectMeta metav1.ObjectMeta) {
				expectedCacheObjectMeta := metav1.ObjectMeta{ResourceVersion: "1", Annotations: map[string]string{"a": "b"}}
				if !reflect.DeepEqual(cacheObjectMeta, expectedCacheObjectMeta) {
					ts.Errorf("received metadata differs from the expected one :\n%s", cmp.Diff(cacheObjectMeta, expectedCacheObjectMeta))
				}

				expectedLocalObjectMeta := metav1.ObjectMeta{ResourceVersion: "2", Annotations: map[string]string{"a": "b"}}
				if !reflect.DeepEqual(localObjectMeta, expectedLocalObjectMeta) {
					ts.Errorf("local object's metadata mustn't be changed, diff :\n%s", cmp.Diff(localObjectMeta, expectedLocalObjectMeta))
				}
			},
		},
		{
			name:                    "annotations on local diff and cached has a shard name",
			cacheObjectMeta:         metav1.ObjectMeta{ResourceVersion: "1", Annotations: map[string]string{"a": "b", "kcp.dev/shard": "amber"}},
			localObjectMeta:         metav1.ObjectMeta{ResourceVersion: "2", Annotations: map[string]string{"a": "b", "foo": "bar"}},
			expectObjectMetaChanged: true,
			validateCacheObjectMeta: func(ts *testing.T, cacheObjectMeta, localObjectMeta metav1.ObjectMeta) {
				expectedCacheObjectMeta := metav1.ObjectMeta{ResourceVersion: "1", Annotations: map[string]string{"a": "b", "kcp.dev/shard": "amber", "foo": "bar"}}
				if !reflect.DeepEqual(cacheObjectMeta, expectedCacheObjectMeta) {
					ts.Errorf("received metadata differs from the expected one :\n%s", cmp.Diff(cacheObjectMeta, expectedCacheObjectMeta))
				}

				expectedLocalObjectMeta := metav1.ObjectMeta{ResourceVersion: "2", Annotations: map[string]string{"a": "b", "foo": "bar"}}
				if !reflect.DeepEqual(localObjectMeta, expectedLocalObjectMeta) {
					ts.Errorf("local object's metadata mustn't be changed, diff :\n%s", cmp.Diff(localObjectMeta, expectedLocalObjectMeta))
				}
			},
		},
		{
			name:                    "no annotations on local, shard name is preserved on cached",
			cacheObjectMeta:         metav1.ObjectMeta{ResourceVersion: "1", Annotations: map[string]string{"kcp.dev/shard": "amber"}},
			localObjectMeta:         metav1.ObjectMeta{},
			expectObjectMetaChanged: true,
			validateCacheObjectMeta: func(ts *testing.T, cacheObjectMeta, localObjectMeta metav1.ObjectMeta) {
				expectedCacheObjectMeta := metav1.ObjectMeta{ResourceVersion: "1", Annotations: map[string]string{"kcp.dev/shard": "amber"}}
				if !reflect.DeepEqual(cacheObjectMeta, expectedCacheObjectMeta) {
					ts.Errorf("received metadata differs from the expected one :\n%s", cmp.Diff(cacheObjectMeta, expectedCacheObjectMeta))
				}

				expectedLocalObjectMeta := metav1.ObjectMeta{}
				if !reflect.DeepEqual(localObjectMeta, expectedLocalObjectMeta) {
					ts.Errorf("local object's metadata mustn't be changed, diff :\n%s", cmp.Diff(localObjectMeta, expectedLocalObjectMeta))
				}
			},
		},
		{
			name:                    "an arbitrary field on meta",
			cacheObjectMeta:         metav1.ObjectMeta{ResourceVersion: "1", Annotations: map[string]string{"kcp.dev/shard": "amber"}},
			localObjectMeta:         metav1.ObjectMeta{Finalizers: []string{"f1"}},
			expectObjectMetaChanged: true,
			validateCacheObjectMeta: func(ts *testing.T, cacheObjectMeta, localObjectMeta metav1.ObjectMeta) {
				expectedCacheObjectMeta := metav1.ObjectMeta{ResourceVersion: "1", Annotations: map[string]string{"kcp.dev/shard": "amber"}, Finalizers: []string{"f1"}}
				if !reflect.DeepEqual(cacheObjectMeta, expectedCacheObjectMeta) {
					ts.Errorf("received metadata differs from the expected one :\n%s", cmp.Diff(cacheObjectMeta, expectedCacheObjectMeta))
				}

				expectedLocalObjectMeta := metav1.ObjectMeta{Finalizers: []string{"f1"}}
				if !reflect.DeepEqual(localObjectMeta, expectedLocalObjectMeta) {
					ts.Errorf("local object's metadata mustn't be changed, diff :\n%s", cmp.Diff(localObjectMeta, expectedLocalObjectMeta))
				}
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(tt *testing.T) {
			cacheApiExport := &apisv1alpha1.APIExport{ObjectMeta: scenario.cacheObjectMeta}
			unstructuredCacheApiExport, err := toUnstructured(cacheApiExport)
			if err != nil {
				tt.Fatal(err)
			}
			localApiExport := &apisv1alpha1.APIExport{ObjectMeta: scenario.localObjectMeta}
			unstructuredLocalApiExport, err := toUnstructured(localApiExport)
			if err != nil {
				tt.Fatal(err)
			}
			metaChanged, err := ensureMeta(unstructuredCacheApiExport, unstructuredLocalApiExport)
			if err != nil {
				tt.Fatal(err)
			}
			if metaChanged != scenario.expectObjectMetaChanged {
				tt.Fatalf("metadata changed = %v, expected metadata to be changed = %v", metaChanged, scenario.expectObjectMetaChanged)
			}
			if scenario.validateCacheObjectMeta != nil {
				cacheApiExportFromUnstructured := &apisv1alpha1.APIExport{}
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredCacheApiExport.Object, cacheApiExportFromUnstructured); err != nil {
					tt.Fatalf("failed to convert unstructured to APIExport: %v", err)
				}

				localApiExportFromUnstructured := &apisv1alpha1.APIExport{}
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstructuredLocalApiExport.Object, localApiExportFromUnstructured); err != nil {
					tt.Fatalf("failed to convert unstructured to APIExport: %v", err)
				}

				scenario.validateCacheObjectMeta(tt, cacheApiExportFromUnstructured.ObjectMeta, localApiExportFromUnstructured.ObjectMeta)
			}
		})
	}
}

func TestHandleUnstructuredObjectDeletion(t *testing.T) {
	scenarios := []struct {
		name                        string
		cacheObject                 *apisv1alpha1.APIExport
		validateCacheObjectDeletion func(ts *testing.T, actions []kcptesting.Action)
	}{
		{
			name: "no-op",
		},
		{
			name:        "DeletionTimestamp filed not set on cacheObject",
			cacheObject: newAPIExport("foo"),
			validateCacheObjectDeletion: func(ts *testing.T, actions []kcptesting.Action) {
				wasCacheApiExportValidated := false
				for _, action := range actions {
					if action.Matches("delete", "apiexports") {
						deleteAction := action.(kcptesting.DeleteAction)
						if deleteAction.GetName() != "foo" {
							ts.Fatalf("unexpected APIExport was removed = %v, expected = %v", deleteAction.GetName(), "foo")
						}
						wasCacheApiExportValidated = true
						break
					}
				}
				if !wasCacheApiExportValidated {
					ts.Errorf("an ApiExport on the cache sever wasn't deleted")
				}
			},
		},
		{
			name: "no-op when DeletionTimestamp filed set",
			cacheObject: func() *apisv1alpha1.APIExport {
				t := metav1.NewTime(time.Now())
				apiExport := newAPIExport("foo")
				apiExport.DeletionTimestamp = &t
				return apiExport
			}(),
			validateCacheObjectDeletion: func(ts *testing.T, actions []kcptesting.Action) {
				if len(actions) > 0 {
					ts.Fatalf("didn't expect any API calls, got %v", actions)
				}
			},
		},
		{
			name: "no-op when DeletionTimestamp filed and Finalizers are set",
			cacheObject: func() *apisv1alpha1.APIExport {
				t := metav1.NewTime(time.Now())
				apiExport := newAPIExport("foo")
				apiExport.DeletionTimestamp = &t
				apiExport.Finalizers = []string{"aFinalizer"}
				return apiExport
			}(),
			validateCacheObjectDeletion: func(ts *testing.T, actions []kcptesting.Action) {
				if len(actions) > 0 {
					ts.Fatalf("didn't expect any API calls, got %v", actions)
				}
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(tt *testing.T) {
			var unstructuredCacheObject *unstructured.Unstructured
			var err error
			if scenario.cacheObject != nil {
				unstructuredCacheObject, err = toUnstructured(scenario.cacheObject)
				if err != nil {
					tt.Fatal(err)
				}
			}
			gvr := apisv1alpha1.SchemeGroupVersion.WithResource("apiexports")
			target := &controller{}
			fakeDynamicClient := kcpfakedynamic.NewSimpleDynamicClient(scheme, func() []runtime.Object {
				if unstructuredCacheObject == nil {
					return []runtime.Object{}
				}
				return []runtime.Object{unstructuredCacheObject}
			}()...)
			target.dynamicCacheClient = fakeDynamicClient

			err = target.handleObjectDeletion(context.TODO(), logicalcluster.New("root"), &gvr, unstructuredCacheObject)
			if err != nil {
				tt.Fatal(err)
			}
			if scenario.validateCacheObjectDeletion != nil {
				scenario.validateCacheObjectDeletion(tt, fakeDynamicClient.Actions())
			}
		})
	}
}

// TestToUnstructured test if changing an unstructured obj won't change the original object
func TestToUnstructured(t *testing.T) {
	apiExport := newAPIExport("a1")
	apiExport.Spec.MaximalPermissionPolicy = &apisv1alpha1.MaximalPermissionPolicy{Local: &apisv1alpha1.LocalAPIExportPolicy{}}
	unstructuredApiExport, err := toUnstructured(apiExport)
	if err != nil {
		t.Fatal(err)
	}

	// manipulate map (a reference type)
	if err := unstructured.SetNestedField(unstructuredApiExport.Object, "valForNewAnnotation", "metadata", "annotations", "newAnnotation"); err != nil {
		t.Fatal(err)
	}
	if newAnnotationVal := unstructuredApiExport.GetAnnotations()["newAnnotation"]; newAnnotationVal != "valForNewAnnotation" {
		t.Fatalf("unexpected value %v for newAnnotation", newAnnotationVal)
	}

	if _, hasNewAnnotation := apiExport.Annotations["newAnnotation"]; hasNewAnnotation {
		t.Fatal("didn't expect changing unstructuredApiExport annotation will also change the original apiExport object")
	}

	// manipulate string (a simple type)
	if err := unstructured.SetNestedField(unstructuredApiExport.Object, "newName", "metadata", "name"); err != nil {
		t.Fatal(err)
	}
	if unstructuredApiExport.GetName() != "newName" {
		t.Fatalf("unexpected name %v", unstructuredApiExport.GetName())
	}
	if apiExport.Name != "a1" {
		t.Fatal("didn't expect changing unstructuredApiExport name will also change the original apiExport object")
	}

	// manipulate pinter (a reference type)
	unstructured.RemoveNestedField(unstructuredApiExport.Object, "spec", "maximalPermissionPolicy")
	_, maximalPolicyFound, err := unstructured.NestedFieldNoCopy(unstructuredApiExport.Object, "spec", "maximalPermissionPolicy")
	if err != nil {
		t.Fatal(err)
	}
	if maximalPolicyFound {
		t.Fatal("didn't expect to find spec.maximalPermissionPolicy")
	}
	if apiExport.Spec.MaximalPermissionPolicy == nil {
		t.Fatal("apiExport.Spec.MaximalPermissionPolicy was removed")
	}
}
