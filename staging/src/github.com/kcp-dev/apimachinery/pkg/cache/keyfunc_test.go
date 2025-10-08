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
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func TestDeletionHandlingMetaClusterNamespaceKeyFunc(t *testing.T) {
	var testCases = []struct {
		name        string
		obj         interface{}
		expected    string
		expectedErr error
	}{
		{
			name: "normal object",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:   "namespace",
					Name:        "name",
					Annotations: map[string]string{logicalcluster.AnnotationKey: "cluster"},
				},
			},
			expected: "cluster|namespace/name",
		},
		{
			name:        "invalid object",
			obj:         "invalid",
			expectedErr: errors.New("object has no meta: object does not implement the Object interfaces"),
		},
		{
			name: "tombstone",
			obj: cache.DeletedFinalStateUnknown{
				Key: "whatever",
			},
			expected: "whatever",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual, actualErr := DeletionHandlingMetaClusterNamespaceKeyFunc(testCase.obj)
			if diff := cmp.Diff(actualErr, testCase.expectedErr, equateErrorMessage); diff != "" {
				t.Errorf("%s: invalid error: %v", testCase.name, diff)
				return
			}
			if diff := cmp.Diff(actual, testCase.expected); diff != "" {
				t.Errorf("%s: invalid key: %v", testCase.name, diff)
			}
		})
	}
}

func TestMetaClusterNamespaceKeyFunc(t *testing.T) {
	var testCases = []struct {
		name        string
		obj         interface{}
		expected    string
		expectedErr error
	}{
		{
			name: "normal object",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:   "namespace",
					Name:        "name",
					Annotations: map[string]string{logicalcluster.AnnotationKey: "cluster"},
				},
			},
			expected: "cluster|namespace/name",
		},
		{
			name: "object without namespace",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "name",
					Annotations: map[string]string{logicalcluster.AnnotationKey: "cluster"},
				},
			},
			expected: "cluster|name",
		},
		{
			name: "object without cluster",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "namespace",
					Name:      "name",
				},
			},
			expected: "namespace/name",
		},
		{
			name: "object without cluster or namespace",
			obj: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "name",
				},
			},
			expected: "name",
		},
		{
			name:        "invalid object",
			obj:         "invalid",
			expectedErr: errors.New("object has no meta: object does not implement the Object interfaces"),
		},
		{
			name:     "explicit key",
			obj:      cache.ExplicitKey("whatever"),
			expected: "whatever",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actual, actualErr := MetaClusterNamespaceKeyFunc(testCase.obj)
			if diff := cmp.Diff(actualErr, testCase.expectedErr, equateErrorMessage); diff != "" {
				t.Errorf("%s: invalid error: %v", testCase.name, diff)
				return
			}
			if diff := cmp.Diff(actual, testCase.expected); diff != "" {
				t.Errorf("%s: invalid key: %v", testCase.name, diff)
			}
		})
	}
}

func TestSplitMetaClusterNamespaceKey(t *testing.T) {
	var testCases = []struct {
		name              string
		key               string
		expectedCluster   logicalcluster.Name
		expectedNamespace string
		expectedName      string
		expectedErr       error
	}{
		{
			name:              "fully populated key",
			key:               "clusterName|namespace/name",
			expectedCluster:   logicalcluster.Name("clusterName"),
			expectedNamespace: "namespace",
			expectedName:      "name",
		},
		{
			name:            "cluster-scoped resource",
			key:             "clusterName|name",
			expectedCluster: logicalcluster.Name("clusterName"),
			expectedName:    "name",
		},
		{
			name:              "single-cluster, namespaced context",
			key:               "namespace/name",
			expectedNamespace: "namespace",
			expectedName:      "name",
		},
		{
			name:         "single-cluster, cluster-scoped context",
			key:          "name",
			expectedName: "name",
		},
		{
			name:        "invalid fully",
			key:         "|||",
			expectedErr: errors.New(`unexpected key format: "|||"`),
		},
		{
			name:            "valid cluster, invalid key",
			key:             "root:something|//2",
			expectedCluster: logicalcluster.Name("root:something"),
			expectedErr:     errors.New(`unexpected key format: "root:something|//2"`),
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			actualCluster, actualNamespace, actualName, actualErr := SplitMetaClusterNamespaceKey(testCase.key)
			if diff := cmp.Diff(actualErr, testCase.expectedErr, equateErrorMessage); diff != "" {
				t.Errorf("%s: invalid error: %v", testCase.name, diff)
				return
			}
			if diff := cmp.Diff(actualCluster, testCase.expectedCluster); diff != "" {
				t.Errorf("%s: invalid cluster: %v", testCase.name, diff)
			}
			if diff := cmp.Diff(actualNamespace, testCase.expectedNamespace); diff != "" {
				t.Errorf("%s: invalid namespace: %v", testCase.name, diff)
			}
			if diff := cmp.Diff(actualName, testCase.expectedName); diff != "" {
				t.Errorf("%s: invalid name: %v", testCase.name, diff)
			}
		})
	}
}

var (
	// equateErrorMessage reports errors to be equal if both are nil
	// or both have the same message.
	equateErrorMessage = cmp.FilterValues(func(x, y interface{}) bool {
		_, ok1 := x.(error)
		_, ok2 := y.(error)
		return ok1 && ok2
	}, cmp.Comparer(func(x, y interface{}) bool {
		xe := x.(error)
		ye := y.(error)
		if xe == nil || ye == nil {
			return xe == nil && ye == nil
		}
		return xe.Error() == ye.Error()
	}))
)
