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

package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func TestSyncerStorageProcess(t *testing.T) {
	tests := map[string]struct {
		pvc *corev1.PersistentVolumeClaim
		pv  *corev1.PersistentVolume
		// wantErr            bool
		errorIs error
	}{
		"PVC just got created in KCP - set the delay sync annotation": {
			pvc:     pvc("foo", "ns", "cluster1", "", nil, nil, ""),
			errorIs: nil,
		},
		"PVC is pending": {
			pvc:     pvc("foo", "ns", "cluster1", "", nil, nil, corev1.ClaimPending),
			errorIs: nil,
		},
		"PVC is lost": {
			pvc:     pvc("foo", "ns", "cluster1", "", nil, nil, corev1.ClaimLost),
			errorIs: nil,
		},
		"PVC is bound but no nslocator": {
			pvc:     pvc("foo", "ns", "cluster1", "foo", nil, nil, corev1.ClaimBound),
			pv:      pv("foo", "ns", "cluster1", nil, nil),
			errorIs: errors.New("downstream PersistentVolumeClaim \"foo\" does not have the \"kcp.io/namespace-locator\" annotation"),
		},
		"PVC is bound but no volume name": {
			pvc:     pvc("foo", "ns", "cluster1", "", nil, map[string]string{"kcp.io/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`}, corev1.ClaimBound),
			pv:      pv("foo", "ns", "cluster1", nil, nil),
			errorIs: errors.New("downstream PersistentVolumeClaim \"foo\" does not have a volume name"),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			syncTargetWorkspace := logicalcluster.Name("root:org:ws")
			syncTargetName := "us-west1"
			syncTarget := syncTargetSpec{name: syncTargetName, workspace: syncTargetWorkspace, key: workloadv1alpha1.ToSyncTargetKey(syncTargetWorkspace, syncTargetName)}
			nsController := PersistentVolumeClaimController{
				syncTarget: syncTarget,
				getDownstreamPersistentVolumeClaim: func(name, namespace string) (runtime.Object, error) {
					o, err := runtime.DefaultUnstructuredConverter.ToUnstructured(tc.pvc)
					if err != nil {
						return nil, err
					}
					return &unstructured.Unstructured{Object: o}, nil
				},
				getDownstreamPersistentVolume: func(name string) (runtime.Object, error) {
					o, err := runtime.DefaultUnstructuredConverter.ToUnstructured(tc.pv)
					if err != nil {
						return nil, err
					}
					return &unstructured.Unstructured{Object: o}, nil
				},
				getUpstreamPersistentVolumeClaim: func(clusterName logicalcluster.Name, persistentVolumeClaimName string, persistentVolumeClaimNamespace string) (runtime.Object, error) {
					o, err := runtime.DefaultUnstructuredConverter.ToUnstructured(tc.pv)
					if err != nil {
						return nil, err
					}
					return &unstructured.Unstructured{Object: o}, nil
				},
				updateDownstreamPersistentVolume: func(ctx context.Context, pv *corev1.PersistentVolume) (*corev1.PersistentVolume, error) {
					return pv, nil
				},
				getDownstreamNamespace: func(name string) (runtime.Object, error) {
					o, err := runtime.DefaultUnstructuredConverter.ToUnstructured(ns())
					if err != nil {
						return nil, err
					}
					return &unstructured.Unstructured{Object: o}, nil
				},
				commit: func(ctx context.Context, r *Resource, p *Resource, namespace string) error {
					return nil
				},
			}

			err := nsController.process(ctx, "foo/bar")
			if err != nil && err.Error() != tc.errorIs.Error() {
				t.Errorf("process() error = %+v, wantErr %+v", err, tc.errorIs)
				return
			}
		})
	}
}

func pv(name, namespace, clusterName string, labels, annotations map[string]string) *corev1.PersistentVolume {
	if clusterName != "" {
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[logicalcluster.AnnotationKey] = clusterName
	}

	if labels == nil {
		labels = make(map[string]string)
	}

	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
		},
	}
}

func pvc(name, namespace, clusterName, volName string, labels, annotations map[string]string, statusPhase corev1.PersistentVolumeClaimPhase) *corev1.PersistentVolumeClaim {
	if clusterName != "" {
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[logicalcluster.AnnotationKey] = clusterName
	}

	if labels == nil {
		labels = make(map[string]string)
	}

	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase: statusPhase,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			VolumeName: volName,
		},
	}
}

func ns() *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-ns",
			Annotations: map[string]string{
				"kcp.io/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
			},
		},
	}
}
