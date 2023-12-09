/*
Copyright 2024 The KCP Authors.

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

package workspacemounts

import (
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

func TestGetWorkspaceKey(t *testing.T) {
	test := []struct {
		obj         interface{}
		expectedKey string
	}{
		{
			obj:         cache.ExplicitKey("test"),
			expectedKey: "workspace::test",
		},
		{
			obj: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "name",
					Namespace: "namespace",
					Annotations: map[string]string{
						"kcp.io/cluster": "cluster",
					},
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Workspace",
					APIVersion: "tenancy.kcp.dev/v1alpha1",
				},
			},
			expectedKey: "workspace::cluster|namespace/name",
		},
		{
			obj: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "name",
					Annotations: map[string]string{
						"kcp.io/cluster": "cluster",
					},
				},
				TypeMeta: metav1.TypeMeta{
					Kind:       "Workspace",
					APIVersion: "tenancy.kcp.dev/v1alpha1",
				},
			},
			expectedKey: "workspace::cluster|name",
		},
	}
	for _, tt := range test {
		tt := tt
		t.Run(tt.expectedKey, func(t *testing.T) {
			key, err := getWorkspaceKey(tt.obj)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if key != tt.expectedKey {
				t.Errorf("unexpected key value, got: %s, want: %s", key, tt.expectedKey)
			}
		})
	}
}

func TestGetWorkspaceKeyFromCluster(t *testing.T) {
	test := struct {
		Cluster  logicalcluster.Name
		Name     string
		expected string
	}{
		Cluster:  "cluster",
		Name:     "name",
		expected: "workspace::cluster|name",
	}
	key := getWorkspaceKeyFromCluster(test.Cluster, test.Name)
	if key != test.expected {
		t.Errorf("unexpected key value, got: %s, want: %s", key, test.expected)
	}
}

func TestGetGVKKey(t *testing.T) {
	tests := []struct {
		gvr        schema.GroupVersionResource
		obj        interface{}
		expected   string
		shouldFail bool
	}{
		{
			gvr: schema.GroupVersionResource{
				Resource: "workspace",
				Version:  "v1alpha1",
				Group:    "tenancy.kcp.dev",
			},
			obj: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "name",
					Namespace: "namespace",
					Annotations: map[string]string{
						"kcp.io/cluster": "cluster",
					},
				},
			},
			expected: "gvr::workspace.v1alpha1.tenancy.kcp.dev::cluster|namespace/name",
		},
		{
			gvr: schema.GroupVersionResource{
				Resource: "mount",
				Version:  "v1alpha1",
				Group:    "mount.external.kcp.dev",
			},
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"kind":       "Mount",
					"apiVersion": "mount.external.kcp.dev/v1alpha1",
					"metadata": map[string]interface{}{
						"name":      "name",
						"namespace": "namespace",
						"annotations": map[string]interface{}{
							"kcp.io/cluster": "cluster",
						},
					},
				},
			},
			expected: "gvr::mount.v1alpha1.mount.external.kcp.dev::cluster|namespace/name",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.gvr.String(), func(t *testing.T) {
			key, err := getGVKKey(tt.gvr, tt.obj)
			if err != nil {
				if !tt.shouldFail {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if key != tt.expected {
					t.Errorf("unexpected key value, got: %s, want: %s", key, tt.expected)
				}
			}
		})
	}
}

func TestParseWorkspaceKey(t *testing.T) {
	tests := []struct {
		key             string
		expectedCluster logicalcluster.Name
		expectedName    string
		shouldFail      bool
	}{
		{
			key:             "workspace::cluster|namespace/name",
			expectedCluster: "cluster",
			expectedName:    "name",
		},
		{
			key:             "workspace::cluster|name",
			expectedCluster: "cluster",
			expectedName:    "name",
		},
		{
			key:             "workspace::test",
			expectedCluster: "",
			expectedName:    "test",
		},
		{
			key:        "bob::test",
			shouldFail: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.key, func(t *testing.T) {
			cluster, name, err := parseWorkspaceKey(tt.key)
			if err != nil {
				if !tt.shouldFail {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if cluster != tt.expectedCluster {
					t.Errorf("unexpected cluster value, got: %s, want: %s", cluster, tt.expectedCluster)
				}
				if name != tt.expectedName {
					t.Errorf("unexpected name value, got: %s, want: %s", name, tt.expectedName)
				}
			}
		})
	}
}

func TestParseGVKKey(t *testing.T) {
	tests := []struct {
		key          string
		expectedgvr  schema.GroupVersionResource
		expectedName string
		shouldFail   bool
	}{
		{
			key:          "gvr::workspace.v1alpha1.tenancy.kcp.dev::cluster|namespace/name",
			expectedgvr:  schema.GroupVersionResource{Resource: "workspace", Version: "v1alpha1", Group: "tenancy.kcp.dev"},
			expectedName: "cluster|namespace/name",
		},
		{
			key:          "gvr::mount.v1alpha1.mount.external.kcp.dev::cluster|namespace/name",
			expectedgvr:  schema.GroupVersionResource{Resource: "mount", Version: "v1alpha1", Group: "mount.external.kcp.dev"},
			expectedName: "cluster|namespace/name",
		},
		{
			key:        "bob::test",
			shouldFail: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.key, func(t *testing.T) {
			gvr, name, err := parseGVKKey(tt.key)
			if err != nil {
				if !tt.shouldFail {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if gvr != tt.expectedgvr {
					t.Errorf("unexpected gvr value, got: %v, want: %v", gvr, tt.expectedgvr)
				}
				if name != tt.expectedName {
					t.Errorf("unexpected name value, got: %s, want: %s", name, tt.expectedName)
				}
			}
		})
	}
}
