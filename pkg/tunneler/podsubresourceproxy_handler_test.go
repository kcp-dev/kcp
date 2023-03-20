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

package tunneler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/request"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func TestPodSubresourceProxyingHandler(t *testing.T) {
	tests := map[string]struct {
		subresource         string
		workspace           string
		podExists           bool
		podIsUpsynced       bool
		syncTargetExists    bool
		synctargetWorkspace string
		expectedError       string
		expectedProxiedPath string
	}{
		"valid request with existing pod and synctarget, pod and synctarget on the same workspace": {
			subresource:         "exec",
			workspace:           "cluster1",
			podExists:           true,
			syncTargetExists:    true,
			podIsUpsynced:       true,
			synctargetWorkspace: "cluster1",
			expectedProxiedPath: "/api/v1/namespaces/kcp-xwdjipyflk7g/pods/foo/exec",
		},
		"valid request with existing pod and synctarget, pod and synctarget on different workspaces": {
			subresource:         "exec",
			workspace:           "cluster1",
			podExists:           true,
			podIsUpsynced:       true,
			syncTargetExists:    true,
			synctargetWorkspace: "cluster2",
			expectedProxiedPath: "/api/v1/namespaces/kcp-1kdcree89tsy/pods/foo/exec",
		},
		"non existing pod": {
			subresource:   "exec",
			workspace:     "cluster1",
			podExists:     false,
			expectedError: "404 Not Found",
		},
		"non existing synctarget": {
			subresource:      "exec",
			workspace:        "cluster1",
			podExists:        true,
			podIsUpsynced:    true,
			syncTargetExists: false,
			expectedError:    "503 Service Unavailable",
		},
		"valid request but pod is not upsynced": {
			subresource:      "exec",
			workspace:        "cluster1",
			podExists:        true,
			podIsUpsynced:    false,
			syncTargetExists: true,
			expectedError:    "400 Bad Request",
		},
		"invalid subresource, expect error": {
			subresource:   "invalid",
			workspace:     "cluster1",
			podExists:     true,
			expectedError: "400 Bad Request",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			proxiedPath := ""
			handler := &subresourceTunnelHandler{
				proxyFunc: func(cluster logicalcluster.Name, syncTargetName string, w http.ResponseWriter, req *http.Request) {
					proxiedPath = req.URL.Path
					if tc.syncTargetExists && tc.podExists {
						w.WriteHeader(http.StatusOK)
						fmt.Fprintln(w, nil)
					}
				},
				apiHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
				getByName: func(ctx context.Context, resource string, cluster logicalcluster.Name, namespace, podName string) (*unstructured.Unstructured, error) {
					if !tc.podExists {
						return nil, errors.NewNotFound(schema.GroupResource{Resource: "pods"}, podName)
					}
					stateLabel := "Upsync"
					if !tc.podIsUpsynced {
						stateLabel = "Synced"
					}
					return &unstructured.Unstructured{
						Object: map[string]interface{}{
							"metadata": map[string]interface{}{
								"name":      podName,
								"namespace": namespace,
								"labels": map[string]interface{}{
									"state.workload.kcp.io/ABCDEFGHIJKL": stateLabel,
								},
							},
						},
					}, nil
				},
				getSyncTargetBySynctargetKey: func(ctx context.Context, synctargetKey string) (*workloadv1alpha1.SyncTarget, error) {
					if !tc.syncTargetExists {
						return nil, errors.NewNotFound(schema.GroupResource{Resource: "synctargets"}, synctargetKey)
					}
					return &workloadv1alpha1.SyncTarget{
						ObjectMeta: metav1.ObjectMeta{
							Name: "synctarget1",
							Annotations: map[string]string{
								"workload.kcp.io/key": "ABCDEFGHIJKL",
								"kcp.io/cluster":      tc.synctargetWorkspace,
							},
						},
					}, nil
				},
			}
			namespace := "default"
			podName := "foo"
			path, err := url.JoinPath("/api/v1/namespaces" + namespace + "/pods/" + podName + "/" + tc.subresource)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(http.MethodGet, path, nil).WithContext(request.WithRequestInfo(
				request.WithCluster(ctx, request.Cluster{Name: logicalcluster.Name(tc.workspace)}),
				&request.RequestInfo{
					Verb:              "get",
					Resource:          "pods",
					APIGroup:          "",
					APIVersion:        "v1",
					Name:              podName,
					Namespace:         namespace,
					IsResourceRequest: true,
					Subresource:       tc.subresource,
					Path:              path,
				}))

			rw := httptest.NewRecorder()
			handler.ServeHTTP(rw, r)
			result := rw.Result()
			defer result.Body.Close()
			bytes, err := io.ReadAll(result.Body)
			require.NoError(t, err, "Request body cannot be read")
			if tc.expectedError != "" {
				require.Equal(t, tc.expectedError, result.Status, "Unexpected status code: %s", string(bytes))
				return
			}
			require.Equal(t, http.StatusOK, result.StatusCode, "Unexpected status code: %s", string(bytes))
			if tc.expectedProxiedPath != "" {
				require.Equal(t, tc.expectedProxiedPath, proxiedPath, "Unexpected proxied path")
			}
		})
	}
}
