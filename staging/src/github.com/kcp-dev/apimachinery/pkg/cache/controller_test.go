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

package cache

import (
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClusterIndexFunc(t *testing.T) {
	tests := map[string]struct {
		obj     *metav1.ObjectMeta
		desired string
	}{
		"bare cluster": {
			obj: &metav1.ObjectMeta{
				Namespace:   "",
				Name:        "name",
				Annotations: map[string]string{logicalcluster.AnnotationKey: "test"},
			},
			desired: "test",
		},
		"cluster and namespace": {
			obj: &metav1.ObjectMeta{
				Namespace:   "namespace",
				Name:        "name",
				Annotations: map[string]string{logicalcluster.AnnotationKey: "test"},
			},
			desired: "test",
		},
		"bare cluster with dashes": {
			obj: &metav1.ObjectMeta{
				Namespace:   "",
				Name:        "name",
				Annotations: map[string]string{logicalcluster.AnnotationKey: "test-with-dashes"},
			},
			desired: "test-with-dashes",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := ClusterIndexFunc(tt.obj)
			require.NoError(t, err, "unexpected error calling ClusterIndexFunc")
			require.Len(t, result, 1, "ClusterIndexFunc should return one result")
			require.Equal(t, result[0], tt.desired)

			key := ClusterIndexKey(logicalcluster.From(tt.obj))

			require.Equal(t, result[0], key, "ClusterIndexFunc and ClusterIndexKey functions have diverged")
		})
	}
}

func TestClusterAndNamespaceIndexFunc(t *testing.T) {
	tests := map[string]struct {
		obj     *metav1.ObjectMeta
		desired string
	}{
		"bare cluster": {
			obj: &metav1.ObjectMeta{
				Namespace:   "",
				Name:        "name",
				Annotations: map[string]string{logicalcluster.AnnotationKey: "test"},
			},
			desired: "test/",
		},
		"cluster and namespace": {
			obj: &metav1.ObjectMeta{
				Namespace:   "testing",
				Name:        "name",
				Annotations: map[string]string{logicalcluster.AnnotationKey: "test"},
			},
			desired: "test/testing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.desired, func(t *testing.T) {
			result, err := ClusterAndNamespaceIndexFunc(tt.obj)
			require.NoError(t, err, "unexpected error calling ClusterAndNamespaceIndexFunc")
			require.Len(t, result, 1, "ClusterIndexFunc should return one result")
			require.Equal(t, result[0], tt.desired)

			key := ClusterAndNamespaceIndexKey(logicalcluster.From(tt.obj), tt.obj.GetNamespace())

			require.Equal(t, result[0], key, "ClusterAndNamespaceIndexFunc and ClusterAndNamespaceIndexKey functions have diverged")
		})
	}
}
