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

package resource

import (
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/util/sets"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

func namespace(annotations, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: annotations,
			Labels:      labels,
		},
	}
}

func object(annotations, labels map[string]string, finalizers []string, deletionTimestamp *metav1.Time, namespace string) metav1.Object {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Annotations:       annotations,
			Labels:            labels,
			DeletionTimestamp: deletionTimestamp,
			Finalizers:        finalizers,
			Namespace:         namespace,
		},
	}
}

func TestComputePlacement(t *testing.T) {
	tests := []struct {
		name                string
		ns                  *corev1.Namespace
		obj                 metav1.Object
		wantAnnotationPatch map[string]interface{} // nil means delete
		wantLabelPatch      map[string]interface{} // nil means delete
	}{
		{name: "unscheduled namespace and object",
			ns:  namespace(nil, nil),
			obj: object(nil, nil, nil, nil, "ns"),
		},
		{name: "pending namespace, unscheduled object",
			ns: namespace(nil, map[string]string{
				"state.workload.kcp.io/cluster-1": "",
			}),
			obj: object(nil, nil, nil, nil, "ns"),
		},
		{name: "invalid state value on namespace",
			ns: namespace(nil, map[string]string{
				"state.workload.kcp.io/cluster-1": "Foo",
			}),
			obj: object(nil, nil, nil, nil, "ns"),
		},
		{name: "syncing namespace, unscheduled object",
			ns: namespace(nil, map[string]string{
				"state.workload.kcp.io/cluster-1": "Sync",
			}),
			obj: object(nil, nil, nil, nil, "ns"),
			wantLabelPatch: map[string]interface{}{
				"state.workload.kcp.io/cluster-1": "Sync",
			},
		},
		{name: "syncing but deleting namespace, unscheduled object, don't schedule the object at all",
			ns: namespace(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-1": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.io/cluster-1": "Sync",
			}),
			obj: object(nil, nil, nil, nil, "ns"),
		},
		{name: "new location on namespace",
			ns: namespace(nil, map[string]string{
				"state.workload.kcp.io/cluster-1": "Sync",
				"state.workload.kcp.io/cluster-2": "Sync",
			}),
			obj: object(nil, map[string]string{
				"state.workload.kcp.io/cluster-1": "Sync",
			}, nil, nil, "ns"),
			wantLabelPatch: map[string]interface{}{
				"state.workload.kcp.io/cluster-2": "Sync",
			},
		},
		{name: "new deletion on namespace",
			ns: namespace(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-4": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.io/cluster-4": "Sync",
			}),
			obj: object(nil, map[string]string{
				"state.workload.kcp.io/cluster-4": "Sync",
			}, nil, nil, "ns"),
			wantLabelPatch: nil,
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.io/cluster-4": "2002-10-02T10:00:00-05:00",
			},
		},
		{name: "existing deletion on namespace and object",
			ns: namespace(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-3": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.io/cluster-3": "Sync",
			}),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-3": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.io/cluster-3": "Sync",
			}, nil, nil, "ns"),
		},
		{name: "hard delete after namespace is not scheduled",
			ns: namespace(nil, nil),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-3": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.io/cluster-3": "Sync", // removed hard because namespace is not scheduled
			}, nil, nil, "ns"),
			wantLabelPatch: map[string]interface{}{
				"state.workload.kcp.io/cluster-3": nil,
			},
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.io/cluster-3": nil,
			},
		},
		{name: "no hard delete after namespace is not scheduled due to the resource having a cluster finalizer, expect no patches",
			ns: namespace(nil, nil),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-3": "2002-10-02T10:00:00-05:00",
				"finalizers.workload.kcp.io/cluster-3":        "external-coordinator",
			}, map[string]string{
				"state.workload.kcp.io/cluster-3": "Sync",
			}, nil, nil, "ns"),
			wantLabelPatch:      nil,
			wantAnnotationPatch: nil,
		},
		{name: "no hard delete after namespace is not scheduled due to the resource having a syncer finalizer, expect no patches",
			ns: namespace(nil, nil),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-3": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.io/cluster-3": "Sync",
			}, []string{
				"workload.kcp.io/syncer-cluster-3",
			}, nil, "ns"),
			wantLabelPatch:      nil,
			wantAnnotationPatch: nil,
		},
		{name: "existing deletion on object, hard delete of namespace",
			ns: namespace(nil, nil),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-3": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.io/cluster-3": "Sync",
			}, nil, nil, "ns"),
			wantLabelPatch: map[string]interface{}{
				"state.workload.kcp.io/cluster-3": nil,
			},
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.io/cluster-3": nil,
			},
		},
		{name: "existing deletion on object, rescheduled namespace",
			ns: namespace(nil, map[string]string{
				"state.workload.kcp.io/cluster-3": "Sync",
			}),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-3": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.io/cluster-3": "Sync",
			}, nil, nil, "ns"),
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.io/cluster-3": nil,
			},
		},
		{name: "multiple locations, added and removed on namespace and object",
			ns: namespace(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-4": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.io/cluster-1": "Sync",
				"state.workload.kcp.io/cluster-2": "Sync",
				"state.workload.kcp.io/cluster-4": "Sync", // deleting
			}),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-3": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.io/cluster-2": "Sync",
				"state.workload.kcp.io/cluster-3": "Sync", // removed hard
				"state.workload.kcp.io/cluster-4": "Sync",
			}, nil, nil, "ns"),
			wantLabelPatch: map[string]interface{}{
				"state.workload.kcp.io/cluster-1": "Sync",
				"state.workload.kcp.io/cluster-3": nil,
			},
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.io/cluster-4": "2002-10-02T10:00:00-05:00",
				"deletion.internal.workload.kcp.io/cluster-3": nil,
			},
		},
		{name: "multiple locations, added and removed on namespace and object, object has a cluster finalizer on cluster-3, expect no changes for that cluster",
			ns: namespace(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-4": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.io/cluster-1": "Sync",
				"state.workload.kcp.io/cluster-2": "Sync",
				"state.workload.kcp.io/cluster-4": "Sync", // deleting
			}),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-3": "2002-10-02T10:00:00-05:00",
				"finalizers.workload.kcp.io/cluster-3":        "external-coordinator",
			}, map[string]string{
				"state.workload.kcp.io/cluster-2": "Sync",
				"state.workload.kcp.io/cluster-3": "Sync",
				"state.workload.kcp.io/cluster-4": "Sync",
			}, nil, nil, "ns"),
			wantLabelPatch: map[string]interface{}{
				"state.workload.kcp.io/cluster-1": "Sync",
			},
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.io/cluster-4": "2002-10-02T10:00:00-05:00",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expectedSynctargetKeys := getLocations(tt.ns.GetLabels(), true)
			expectedDeletedSynctargetKeys := getDeletingLocations(tt.ns.GetAnnotations())
			gotAnnotationPatch, gotLabelPatch := computePlacement(expectedSynctargetKeys, expectedDeletedSynctargetKeys, tt.obj)
			if diff := cmp.Diff(gotAnnotationPatch, tt.wantAnnotationPatch); diff != "" {
				t.Errorf("incorrect annotation patch: %s", diff)
			}
			if diff := cmp.Diff(gotLabelPatch, tt.wantLabelPatch); diff != "" {
				t.Errorf("incorrect label patch: %s", diff)
			}
		})
	}
}

func TestPropagateDeletionTimestamp(t *testing.T) {
	tests := []struct {
		name                string
		obj                 metav1.Object
		wantAnnotationPatch map[string]interface{} // nil means delete
	}{
		{name: "Object is marked for deletion and has one location",
			obj: object(nil, map[string]string{
				"state.workload.kcp.io/cluster-1": "Sync",
			}, nil, &metav1.Time{Time: time.Date(2002, 10, 2, 10, 0, 0, 0, time.UTC)}, "ns"),
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.io/cluster-1": "2002-10-02T10:00:00Z",
			},
		}, {name: "Object is marked for deletion and has multiple locations",
			obj: object(nil, map[string]string{
				"state.workload.kcp.io/cluster-1": "Sync",
				"state.workload.kcp.io/cluster-2": "Sync",
				"state.workload.kcp.io/cluster-3": "Sync",
			}, nil, &metav1.Time{Time: time.Date(2002, 10, 2, 10, 0, 0, 0, time.UTC)}, "ns"),
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.io/cluster-1": "2002-10-02T10:00:00Z",
				"deletion.internal.workload.kcp.io/cluster-2": "2002-10-02T10:00:00Z",
				"deletion.internal.workload.kcp.io/cluster-3": "2002-10-02T10:00:00Z",
			},
		},
		{name: "Object is marked for deletion, has one location with a location deletionTimestamp already set, no change",
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-1": "2002-10-02T10:00:00Z",
			}, map[string]string{
				"state.workload.kcp.io/cluster-1": "Sync",
			}, nil, &metav1.Time{Time: time.Date(2002, 10, 2, 10, 0, 0, 0, time.UTC)}, "ns"),
			wantAnnotationPatch: map[string]interface{}{},
		},
		{name: "Object is marked for deletion, has one location with a location deletionTimestamp already set, but with different time value, no update",
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.io/cluster-1": "2000-01-01 10:00:00 +0000 UTC",
			}, map[string]string{
				"state.workload.kcp.io/cluster-1": "Sync",
			}, nil, &metav1.Time{Time: time.Date(2002, 10, 2, 10, 0, 0, 0, time.UTC)}, "ns"),
			wantAnnotationPatch: map[string]interface{}{},
		},
		{name: "Object is marked for deletion, has one pending location, the deletionTimestamp of that location should be set",
			obj: object(nil, map[string]string{
				"state.workload.kcp.io/cluster-1": "",
			}, nil, &metav1.Time{Time: time.Date(2002, 10, 2, 10, 0, 0, 0, time.UTC)}, "ns"),
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.io/cluster-1": "2002-10-02T10:00:00Z",
			},
		},
		{name: "Object is marked for deletion and has no locations",
			obj:                 object(nil, nil, nil, &metav1.Time{Time: time.Date(2002, 10, 2, 10, 0, 0, 0, time.UTC)}, "ns"),
			wantAnnotationPatch: map[string]interface{}{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAnnotationPatch := propagateDeletionTimestamp(klog.Background(), tt.obj)
			if diff := cmp.Diff(gotAnnotationPatch, tt.wantAnnotationPatch); diff != "" {
				t.Errorf("incorrect annotation patch: %s", diff)
			}
		})
	}
}

func TestGetLocations(t *testing.T) {
	tests := []struct {
		name        string
		labels      map[string]string
		skipPending bool
		wantKeys    []string
	}{
		{name: "No locations",
			labels:      map[string]string{},
			skipPending: false,
			wantKeys:    []string{},
		},
		{name: "One location",
			labels: map[string]string{
				"state.workload.kcp.io/cluster-1": "Sync",
			},
			skipPending: false,
			wantKeys:    []string{"cluster-1"},
		},
		{name: "Multiple locations",
			labels: map[string]string{
				"state.workload.kcp.io/cluster-1": "Sync",
				"state.workload.kcp.io/cluster-2": "Sync",
				"state.workload.kcp.io/cluster-3": "Sync",
			},
			skipPending: false,
			wantKeys:    []string{"cluster-1", "cluster-2", "cluster-3"},
		},
		{name: "Multiple locations, some pending, skipPending false",
			labels: map[string]string{
				"state.workload.kcp.io/cluster-1": "Sync",
				"state.workload.kcp.io/cluster-2": "Sync",
				"state.workload.kcp.io/cluster-3": "Sync",
				"state.workload.kcp.io/cluster-4": "",
				"state.workload.kcp.io/cluster-5": "",
				"state.workload.kcp.io/cluster-6": "",
			},
			skipPending: false,
			wantKeys:    []string{"cluster-1", "cluster-2", "cluster-3", "cluster-4", "cluster-5", "cluster-6"},
		},
		{name: "Multiple locations, some pending, skipPending true",
			labels: map[string]string{
				"state.workload.kcp.io/cluster-1": "Sync",
				"state.workload.kcp.io/cluster-2": "Sync",
				"state.workload.kcp.io/cluster-3": "Sync",
				"state.workload.kcp.io/cluster-4": "",
				"state.workload.kcp.io/cluster-5": "",
				"state.workload.kcp.io/cluster-6": "",
			},
			skipPending: true,
			wantKeys:    []string{"cluster-1", "cluster-2", "cluster-3"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if gotKeys := getLocations(tt.labels, tt.skipPending); !reflect.DeepEqual(sets.List[string](gotKeys), tt.wantKeys) {
				t.Errorf("getLocations() = %v, want %v", gotKeys, tt.wantKeys)
			}
		})
	}
}

func TestGetDeletingLocations(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		wantKeys    map[string]string
	}{
		{name: "No locations",
			annotations: map[string]string{},
			wantKeys:    map[string]string{},
		},
		{name: "One location",
			annotations: map[string]string{
				"deletion.internal.workload.kcp.io/cluster-1": "2002-10-02T10:00:00Z",
			},
			wantKeys: map[string]string{
				"cluster-1": "2002-10-02T10:00:00Z",
			},
		},
		{name: "Multiple locations",
			annotations: map[string]string{
				"deletion.internal.workload.kcp.io/cluster-1": "2002-10-02T10:00:00Z",
				"deletion.internal.workload.kcp.io/cluster-2": "2002-10-02T10:00:00Z",
				"deletion.internal.workload.kcp.io/cluster-3": "2002-10-02T10:00:00Z",
			},
			wantKeys: map[string]string{
				"cluster-1": "2002-10-02T10:00:00Z",
				"cluster-2": "2002-10-02T10:00:00Z",
				"cluster-3": "2002-10-02T10:00:00Z",
			},
		},
		{name: "Multiple locations, other annotations",
			annotations: map[string]string{
				"deletion.internal.workload.kcp.io/cluster-1": "2002-10-02T10:00:00Z",
				"deletion.internal.workload.kcp.io/cluster-2": "2002-10-02T10:00:00Z",
				"deletion.internal.workload.kcp.io/cluster-3": "2002-10-02T10:00:00Z",
				"this.is.not.a.deletion/annotation":           "2002-10-02T10:00:00Z",
			},
			wantKeys: map[string]string{
				"cluster-1": "2002-10-02T10:00:00Z",
				"cluster-2": "2002-10-02T10:00:00Z",
				"cluster-3": "2002-10-02T10:00:00Z",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if gotKeys := getDeletingLocations(tt.annotations); !reflect.DeepEqual(gotKeys, tt.wantKeys) {
				t.Errorf("getDeletingLocations() = %v, want %v", gotKeys, tt.wantKeys)
			}
		})
	}
}
