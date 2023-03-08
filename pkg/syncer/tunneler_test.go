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

package syncer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

func Test_withPodAccessCheck(t *testing.T) {
	tests := []struct {
		name                  string
		podState              string
		subresource           string
		synctargetClusterName string
		synctargetName        string
		syncTargetUID         string
		expectedStatusCode    int
		requestPath           string
	}{
		{
			name:                  "valid pod and valid namespace, expect success",
			expectedStatusCode:    http.StatusOK,
			podState:              "Upsync",
			synctargetName:        "test-synctarget",
			syncTargetUID:         "test-uid",
			synctargetClusterName: "test-workspace",
			requestPath:           "/api/v1/namespaces/test-namespace/pods/test-pod/log",
		},
		{
			name:                  "pod not in upsync state, expect 403",
			expectedStatusCode:    http.StatusForbidden,
			podState:              "",
			synctargetName:        "test-synctarget",
			syncTargetUID:         "test-uid",
			synctargetClusterName: "test-workspace",
			requestPath:           "/api/v1/namespaces/test-namespace/pods/test-pod/log",
		},
		{
			name:                  "namespace not owned by the syncer, expect 403",
			expectedStatusCode:    http.StatusForbidden,
			podState:              "Upsync",
			synctargetName:        "test-synctarget",
			syncTargetUID:         "test-another-uid",
			synctargetClusterName: "test-workspace",
			requestPath:           "/api/v1/namespaces/test-namespace/pods/test-pod/log",
		},
		{
			name:                  "non existent pod, expect 403",
			expectedStatusCode:    http.StatusForbidden,
			podState:              "Upsync",
			synctargetName:        "test-synctarget",
			syncTargetUID:         "test-uid",
			synctargetClusterName: "test-workspace",
			requestPath:           "/api/v1/namespaces/test-namespace/pods/not-existing-pod/log",
		},
		{
			name:                  "non existent namespace, expect 403",
			expectedStatusCode:    http.StatusForbidden,
			podState:              "Upsync",
			synctargetName:        "test-synctarget",
			syncTargetUID:         "test-uid",
			synctargetClusterName: "test-workspace",
			requestPath:           "/api/v1/namespaces/not-existing-namespace/pods/test-pod/log",
		},
		{
			name:                  "request is not for a pod, expect 403",
			expectedStatusCode:    http.StatusForbidden,
			podState:              "Upsync",
			synctargetName:        "test-synctarget",
			syncTargetUID:         "test-uid",
			synctargetClusterName: "test-workspace",
			requestPath:           "/api/v1/namespaces/test-namespace/configmaps/test-pod/status",
		},
		{
			name:                  "request doesn't contain a subresource, expect 403",
			expectedStatusCode:    http.StatusForbidden,
			podState:              "Upsync",
			synctargetName:        "test-synctarget",
			syncTargetUID:         "test-uid",
			synctargetClusterName: "test-workspace",
			requestPath:           "/api/v1/namespaces/test-namespace/pods/test-pod",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rw := httptest.NewRecorder()

			fakeDownstreamInformers := map[schema.GroupVersionResource]cache.GenericLister{}
			fakeDownstreamInformers[schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}] = &fakeLister{
				objs: []runtime.Object{
					&corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-pod",
							Namespace: "test-namespace",
							Labels: map[string]string{
								workloadv1alpha1.ClusterResourceStateLabelPrefix + workloadv1alpha1.ToSyncTargetKey(logicalcluster.Name(tt.synctargetClusterName), tt.synctargetName): tt.podState,
							},
						},
					},
				},
			}
			locator, err := json.Marshal(shared.NamespaceLocator{
				SyncTarget: shared.SyncTargetLocator{
					Name:        "test-synctarget",
					UID:         "test-uid",
					ClusterName: "test-workspace",
				},
				ClusterName: "test-workspace",
				Namespace:   "test-namespace",
			})
			require.NoError(t, err)

			fakeDownstreamInformers[schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}] = &fakeLister{
				objs: []runtime.Object{
					&corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: "test-namespace",
							Annotations: map[string]string{
								shared.NamespaceLocatorAnnotation: string(locator),
							},
						},
					},
				},
			}

			synctargetWorkspaceName, _ := logicalcluster.NewPath(tt.synctargetClusterName).Name()
			handler := withPodAccessCheck(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}),
				func(gvr schema.GroupVersionResource) (cache.GenericLister, error) {
					return fakeDownstreamInformers[gvr], nil
				},
				synctargetWorkspaceName, tt.synctargetName, tt.syncTargetUID)

			request := httptest.NewRequest(http.MethodGet, tt.requestPath, nil)
			handler.ServeHTTP(rw, request)

			require.Equal(t, tt.expectedStatusCode, rw.Code)
		})
	}
}

type fakeLister struct {
	objs []runtime.Object
}

func (f *fakeLister) List(selector labels.Selector) (ret []runtime.Object, err error) {
	return f.objs, nil
}

func (f *fakeLister) Get(name string) (ret runtime.Object, err error) {
	for _, obj := range f.objs {
		// return the unstructured object if the name matches
		if obj.(metav1.Object).GetName() == name {
			unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
			if err != nil {
				return nil, err
			}
			return &unstructured.Unstructured{Object: unstructuredObj}, nil
		}
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{}, name)
}

func (f *fakeLister) ByIndex(indexName, indexKey string) (ret []interface{}, err error) {
	panic("implement me")
}

func (f *fakeLister) ByNamespace(namespace string) cache.GenericNamespaceLister {
	for f.objs[0].(metav1.Object).GetNamespace() == namespace {
		return f
	}
	return nil
}
