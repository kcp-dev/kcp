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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

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

func object(annotations, labels map[string]string, finalizers []string, deletionTimestamp *metav1.Time) metav1.Object {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Annotations:       annotations,
			Labels:            labels,
			DeletionTimestamp: deletionTimestamp,
			Finalizers:        finalizers,
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
			obj: object(nil, nil, nil, nil),
		},
		{name: "pending namespace, unscheduled object",
			ns: namespace(nil, map[string]string{
				"state.workload.kcp.dev/cluster-1": "",
			}),
			obj: object(nil, nil, nil, nil),
		},
		{name: "invalid state value on namespace",
			ns: namespace(nil, map[string]string{
				"state.workload.kcp.dev/cluster-1": "Foo",
			}),
			obj: object(nil, nil, nil, nil),
		},
		{name: "syncing namespace, unscheduled object",
			ns: namespace(nil, map[string]string{
				"state.workload.kcp.dev/cluster-1": "Sync",
			}),
			obj: object(nil, nil, nil, nil),
			wantLabelPatch: map[string]interface{}{
				"state.workload.kcp.dev/cluster-1": "Sync",
			},
		},
		{name: "new location on namespace",
			ns: namespace(nil, map[string]string{
				"state.workload.kcp.dev/cluster-1": "Sync",
				"state.workload.kcp.dev/cluster-2": "Sync",
			}),
			obj: object(nil, map[string]string{
				"state.workload.kcp.dev/cluster-1": "Sync",
			}, nil, nil),
			wantLabelPatch: map[string]interface{}{
				"state.workload.kcp.dev/cluster-2": "Sync",
			},
		},
		{name: "new deletion on namespace",
			ns: namespace(map[string]string{
				"deletion.internal.workload.kcp.dev/cluster-4": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.dev/cluster-4": "Sync",
			}),
			obj: object(nil, map[string]string{
				"state.workload.kcp.dev/cluster-4": "Sync",
			}, nil, nil),
			wantLabelPatch: nil,
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.dev/cluster-4": "2002-10-02T10:00:00-05:00",
			},
		},
		{name: "existing deletion on namespace and object",
			ns: namespace(map[string]string{
				"deletion.internal.workload.kcp.dev/cluster-3": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.dev/cluster-3": "Sync",
			}),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.dev/cluster-3": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.dev/cluster-3": "Sync",
			}, nil, nil),
		},
		{name: "hard delete after namespace is not scheduled",
			ns: namespace(nil, nil),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.dev/cluster-3": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.dev/cluster-3": "Sync", // removed hard because namespace is not scheduled
			}, nil, nil),
			wantLabelPatch: map[string]interface{}{
				"state.workload.kcp.dev/cluster-3": nil,
			},
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.dev/cluster-3": nil,
			},
		},
		{name: "no hard delete after namespace is not scheduled due to the resource having a cluster finalizer, expect no patches",
			ns: namespace(nil, nil),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.dev/cluster-3": "2002-10-02T10:00:00-05:00",
				"finalizers.workload.kcp.dev/cluster-3":        "external-coordinator",
			}, map[string]string{
				"state.workload.kcp.dev/cluster-3": "Sync",
			}, nil, nil),
			wantLabelPatch:      nil,
			wantAnnotationPatch: nil,
		},
		{name: "no hard delete after namespace is not scheduled due to the resource having a syncer finalizer, expect no patches",
			ns: namespace(nil, nil),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.dev/cluster-3": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.dev/cluster-3": "Sync",
			}, []string{
				"workload.kcp.dev/syncer-cluster-3",
			}, nil),
			wantLabelPatch:      nil,
			wantAnnotationPatch: nil,
		},
		{name: "existing deletion on object, hard delete of namespace",
			ns: namespace(nil, nil),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.dev/cluster-3": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.dev/cluster-3": "Sync",
			}, nil, nil),
			wantLabelPatch: map[string]interface{}{
				"state.workload.kcp.dev/cluster-3": nil,
			},
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.dev/cluster-3": nil,
			},
		},
		{name: "existing deletion on object, rescheduled namespace",
			ns: namespace(nil, map[string]string{
				"state.workload.kcp.dev/cluster-3": "Sync",
			}),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.dev/cluster-3": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.dev/cluster-3": "Sync",
			}, nil, nil),
		},
		{name: "multiple locations, added and removed on namespace and object",
			ns: namespace(map[string]string{
				"deletion.internal.workload.kcp.dev/cluster-4": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.dev/cluster-1": "Sync",
				"state.workload.kcp.dev/cluster-2": "Sync",
				"state.workload.kcp.dev/cluster-4": "Sync", // deleting
			}),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.dev/cluster-3": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.dev/cluster-2": "Sync",
				"state.workload.kcp.dev/cluster-3": "Sync", // removed hard
				"state.workload.kcp.dev/cluster-4": "Sync",
			}, nil, nil),
			wantLabelPatch: map[string]interface{}{
				"state.workload.kcp.dev/cluster-1": "Sync",
				"state.workload.kcp.dev/cluster-3": nil,
			},
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.dev/cluster-4": "2002-10-02T10:00:00-05:00",
				"deletion.internal.workload.kcp.dev/cluster-3": nil,
			},
		},
		{name: "multiple locations, added and removed on namespace and object, object has a cluster finalizer on cluster-3, expect no changes for that cluster",
			ns: namespace(map[string]string{
				"deletion.internal.workload.kcp.dev/cluster-4": "2002-10-02T10:00:00-05:00",
			}, map[string]string{
				"state.workload.kcp.dev/cluster-1": "Sync",
				"state.workload.kcp.dev/cluster-2": "Sync",
				"state.workload.kcp.dev/cluster-4": "Sync", // deleting
			}),
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.dev/cluster-3": "2002-10-02T10:00:00-05:00",
				"finalizers.workload.kcp.dev/cluster-3":        "external-coordinator",
			}, map[string]string{
				"state.workload.kcp.dev/cluster-2": "Sync",
				"state.workload.kcp.dev/cluster-3": "Sync",
				"state.workload.kcp.dev/cluster-4": "Sync",
			}, nil, nil),
			wantLabelPatch: map[string]interface{}{
				"state.workload.kcp.dev/cluster-1": "Sync",
			},
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.dev/cluster-4": "2002-10-02T10:00:00-05:00",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAnnotationPatch, gotLabelPatch := computePlacement(tt.ns, tt.obj)
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
		annotationPatch     map[string]interface{}
		obj                 metav1.Object
		wantAnnotationPatch map[string]interface{} // nil means delete
	}{
		{name: "Object is marked for deletion and has one location",
			obj: object(nil, map[string]string{
				"state.workload.kcp.dev/cluster-1": "Sync",
			}, nil, &metav1.Time{Time: time.Date(2002, 10, 2, 10, 0, 0, 0, time.UTC)}),
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.dev/cluster-1": "2002-10-02T10:00:00Z",
			},
		}, {name: "Object is marked for deletion and has multiple locations",
			obj: object(nil, map[string]string{
				"state.workload.kcp.dev/cluster-1": "Sync",
				"state.workload.kcp.dev/cluster-2": "Sync",
				"state.workload.kcp.dev/cluster-3": "Sync",
			}, nil, &metav1.Time{Time: time.Date(2002, 10, 2, 10, 0, 0, 0, time.UTC)}),
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.dev/cluster-1": "2002-10-02T10:00:00Z",
				"deletion.internal.workload.kcp.dev/cluster-2": "2002-10-02T10:00:00Z",
				"deletion.internal.workload.kcp.dev/cluster-3": "2002-10-02T10:00:00Z",
			},
		},
		{name: "Object is marked for deletion, has one location and the annotationPatch has some value, should be maintained",
			obj: object(nil, map[string]string{
				"state.workload.kcp.dev/cluster-1": "Sync",
			}, nil, &metav1.Time{Time: time.Date(2002, 10, 2, 10, 0, 0, 0, time.UTC)}),
			annotationPatch: map[string]interface{}{
				"new-annotation-that-we-dont-care-about": "new-value",
			},
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.dev/cluster-1": "2002-10-02T10:00:00Z",
				"new-annotation-that-we-dont-care-about":       "new-value",
			},
		},
		{name: "Object is marked for deletion, has one location with a location deletionTimestamp already set, no change",
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.dev/cluster-1": "2002-10-02T10:00:00Z",
			}, map[string]string{
				"state.workload.kcp.dev/cluster-1": "Sync",
			}, nil, &metav1.Time{Time: time.Date(2002, 10, 2, 10, 0, 0, 0, time.UTC)}),
			wantAnnotationPatch: map[string]interface{}{},
		},
		{name: "Object is marked for deletion, has one location with a location deletionTimestamp already set, but with different time value, no update",
			obj: object(map[string]string{
				"deletion.internal.workload.kcp.dev/cluster-1": "2000-01-01 10:00:00 +0000 UTC",
			}, map[string]string{
				"state.workload.kcp.dev/cluster-1": "Sync",
			}, nil, &metav1.Time{Time: time.Date(2002, 10, 2, 10, 0, 0, 0, time.UTC)}),
			wantAnnotationPatch: map[string]interface{}{},
		},
		{name: "Object is marked for deletion, has one pending location, the deletionTimestamp of that location should be set",
			obj: object(nil, map[string]string{
				"state.workload.kcp.dev/cluster-1": "",
			}, nil, &metav1.Time{Time: time.Date(2002, 10, 2, 10, 0, 0, 0, time.UTC)}),
			wantAnnotationPatch: map[string]interface{}{
				"deletion.internal.workload.kcp.dev/cluster-1": "2002-10-02T10:00:00Z",
			},
		},
		{name: "Object is marked for deletion and has no locations",
			obj:                 object(nil, nil, nil, &metav1.Time{Time: time.Date(2002, 10, 2, 10, 0, 0, 0, time.UTC)}),
			wantAnnotationPatch: map[string]interface{}{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAnnotationPatch := propagateDeletionTimestamp(klog.Background(), tt.obj, tt.annotationPatch)
			if diff := cmp.Diff(gotAnnotationPatch, tt.wantAnnotationPatch); diff != "" {
				t.Errorf("incorrect annotation patch: %s", diff)
			}
		})
	}
}
