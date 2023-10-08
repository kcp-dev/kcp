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
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"
)

func TestReconcile(t *testing.T) {
	gr := schema.GroupResource{Group: "example.com", Resource: "elephants"}
	elephant := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "example.com/v1",
			"kind":       "Elephant",
			"metadata": map[string]interface{}{
				"name":            "dumbo",
				"namespace":       "zoo",
				"resourceVersion": "42",
				"uid":             "47-11",
				"kcp.dev/cluster": "root",
			},
			"spec": map[string]interface{}{
				"color": "pink",
			},
			"status": map[string]interface{}{
				"weight": float64(42),
			},
		},
	}

	scenarios := []struct {
		name          string
		getLocalCopy  func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error)
		getGlobalCopy func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error)

		key string

		expectedCreate          *unstructured.Unstructured
		expectedDeleteName      string
		expectedDeleteNamespace string
		expectedUpdate          *unstructured.Unstructured

		expectedError string
	}{
		{
			name: "case 1: creation of the object in the cache server",
			getLocalCopy: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
				if cluster.String() == "root" && namespace == "zoo" && name == "dumbo" {
					return elephant.DeepCopy(), nil
				}
				return nil, errors.NewNotFound(gr, name)
			},
			getGlobalCopy:  getCopyNotFoundFunc,
			key:            "root|zoo/dumbo",
			expectedCreate: WithShardName(WithoutResourceVersion(elephant.DeepCopy()), "root"),
		},
		{
			name: "case 2: cached object is removed when local object was removed",
			getLocalCopy: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
				return WithDeletionTimestamp(elephant.DeepCopy(), time.Now()), nil
			},
			getGlobalCopy: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
				return WithResourceVersion(WithShardName(elephant.DeepCopy(), "shard-1"), "7"), nil
			},
			key:                     "root|zoo/dumbo",
			expectedDeleteName:      "dumbo",
			expectedDeleteNamespace: "zoo",
		},
		{
			name:         "case 2: cached object is removed when local object was not found",
			getLocalCopy: getCopyNotFoundFunc,
			getGlobalCopy: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
				return elephant.DeepCopy(), nil
			},
			key:                     "root|zoo/dumbo",
			expectedDeleteName:      "dumbo",
			expectedDeleteNamespace: "zoo",
		},
		{
			name: "case 3: update, metadata mismatch",
			getLocalCopy: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
				return WithLabel(elephant.DeepCopy(), "a", "b"), nil
			},
			getGlobalCopy: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
				return WithResourceVersion(elephant.DeepCopy(), "7"), nil
			},
			key:            "root|zoo/dumbo",
			expectedUpdate: WithLabel(WithResourceVersion(elephant.DeepCopy(), "7"), "a", "b"),
		},
		{
			name: "case 3: update, spec changed",
			getLocalCopy: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
				return WithChange(elephant.DeepCopy(), []string{"spec", "color"}, "blue"), nil
			},
			getGlobalCopy: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
				return WithResourceVersion(elephant.DeepCopy(), "7"), nil
			},
			key:            "root|zoo/dumbo",
			expectedUpdate: WithChange(WithResourceVersion(elephant.DeepCopy(), "7"), []string{"spec", "color"}, "blue"),
		},
		{
			name: "case 3: update, status changed",
			getLocalCopy: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
				return WithChange(elephant.DeepCopy(), []string{"status", "weight"}, "42.5"), nil
			},
			getGlobalCopy: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
				return WithResourceVersion(elephant.DeepCopy(), "7"), nil
			},
			key:            "root|zoo/dumbo",
			expectedUpdate: WithChange(WithResourceVersion(elephant.DeepCopy(), "7"), []string{"status", "weight"}, "42.5"),
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(tt *testing.T) {
			var created *unstructured.Unstructured
			var updated *unstructured.Unstructured
			var deletedNamespace, deletedName string

			r := &reconciler{
				shardName:     "root",
				getLocalCopy:  scenario.getLocalCopy,
				getGlobalCopy: scenario.getGlobalCopy,
				createObject: func(ctx context.Context, clusterName logicalcluster.Name, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
					created = obj.DeepCopy()
					return created, nil
				},
				updateObject: func(ctx context.Context, cluster logicalcluster.Name, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
					updated = obj.DeepCopy()
					return updated, nil
				},
				deleteObject: func(ctx context.Context, cluster logicalcluster.Name, ns, name string) error {
					deletedNamespace = ns
					deletedName = name
					return nil
				},
			}

			err := r.reconcile(context.Background(), scenario.key)
			if scenario.expectedError != "" && err == nil {
				tt.Fatalf("expected error %q, got nil", scenario.expectedError)
			} else if scenario.expectedError == "" && err != nil {
				tt.Fatalf("unexpected error: %v", err)
			} else if scenario.expectedError != "" && err != nil && !strings.Contains(err.Error(), scenario.expectedError) {
				tt.Fatalf("expected error %q, got %q", scenario.expectedError, err.Error())
			}

			if scenario.expectedCreate != nil && created == nil {
				tt.Fatalf("expected object to be created, but it was not")
			} else if scenario.expectedCreate == nil && created != nil {
				tt.Fatalf("expected object to not be created, but it was")
			} else if !reflect.DeepEqual(scenario.expectedCreate, created) {
				tt.Fatalf("expected created object to be %v, got %v, diff:\n%s", scenario.expectedCreate, created, cmp.Diff(scenario.expectedCreate, created))
			}

			if scenario.expectedUpdate != nil && updated == nil {
				tt.Fatalf("expected object to be updated, but it was not")
			} else if scenario.expectedUpdate == nil && updated != nil {
				tt.Fatalf("expected object to not be updated, but it was")
			} else if !reflect.DeepEqual(scenario.expectedUpdate, updated) {
				tt.Fatalf("expected updated object to be %v, got %v, diff:\n%s", scenario.expectedUpdate, updated, cmp.Diff(scenario.expectedUpdate, updated))
			}

			if scenario.expectedDeleteName != deletedName {
				tt.Fatalf("expected deleted name %q, got %q", scenario.expectedDeleteName, deletedName)
			}
			if scenario.expectedDeleteNamespace != deletedNamespace {
				tt.Fatalf("expected deleted namespace %q, got %q", scenario.expectedDeleteNamespace, deletedNamespace)
			}
		})
	}
}

func WithResourceVersion(u *unstructured.Unstructured, rv string) *unstructured.Unstructured {
	u.SetResourceVersion(rv)

	return u
}
func WithoutResourceVersion(u *unstructured.Unstructured) *unstructured.Unstructured {
	unstructured.RemoveNestedField(u.Object, "metadata", "resourceVersion")
	return u
}

func WithDeletionTimestamp(u *unstructured.Unstructured, t time.Time) *unstructured.Unstructured {
	ts := metav1.NewTime(t)
	u.SetDeletionTimestamp(&ts)
	return u
}

func WithLabel(u *unstructured.Unstructured, key, value string) *unstructured.Unstructured {
	labels := u.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[key] = value
	u.SetLabels(labels)
	return u
}

func WithShardName(u *unstructured.Unstructured, name string) *unstructured.Unstructured {
	ann := u.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[request.ShardAnnotationKey] = name
	u.SetAnnotations(ann)
	return u
}

func WithChange(u *unstructured.Unstructured, path []string, value interface{}) *unstructured.Unstructured {
	unstructured.SetNestedField(u.Object, value, path...) //nolint:errcheck
	return u
}

func getCopyNotFoundFunc(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
	return nil, errors.NewNotFound(schema.GroupResource{Group: "example.com", Resource: "elephants"}, name)
}
