/*
Copyright 2021 The KCP Authors.

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

package spec

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/dynamic/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clusters"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

var scheme *runtime.Scheme

func init() {
	scheme = runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
}

func TestDeepEqualApartFromStatus(t *testing.T) {
	type args struct {
		a, b *unstructured.Unstructured
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "both objects are equals",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "both objects are equals as are being deleted",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":              "test_kind",
						"apiVersion":        "test_version",
						"deletionTimestamp": "2010-11-10T23:00:00Z",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":              "test_kind",
						"apiVersion":        "test_version",
						"deletionTimestamp": "2010-11-10T23:00:00Z",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
						},
					},
				},
			},
			want: true,
		},
		{
			name: "both objects are equals even they have different statuses",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":              "test_kind",
						"apiVersion":        "test_version",
						"deletionTimestamp": "2010-11-10T23:00:00Z",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
						},
						"status": map[string]interface{}{},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":              "test_kind",
						"apiVersion":        "test_version",
						"deletionTimestamp": "2010-11-10T23:00:00Z",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
						},
						"status": map[string]interface{}{
							"phase": "Failed",
						},
					},
				},
			},
			want: true,
		},
		{
			name: "both objects are equals even they have a different value in InternalClusterStatusAnnotationPrefix",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"annotations": map[string]interface{}{
								workloadv1alpha1.InternalClusterStatusAnnotationPrefix: "2",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"annotations": map[string]interface{}{
								workloadv1alpha1.InternalClusterStatusAnnotationPrefix: "1",
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "not equal as b is missing labels",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
						},
					},
				},
			},
			want: false,
		},
		{
			name: "not equal as b has different labels values",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "another_test_value",
							},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "not equal as b resource is missing annotations",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"annotations": map[string]interface{}{
								"annotation": "this is an annotation",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "not equal as b resource has different annotations",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"annotations": map[string]interface{}{
								"annotation": "this is an annotation",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"annotations": map[string]interface{}{
								"annotation":  "this is an annotation",
								"annotation2": "this is another annotation",
							},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "not equal as b resource has finalizers",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"finalizers": []interface{}{
								"finalizer.1",
								"finalizer.2",
							},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "not equal even objects are equal as A is being deleted",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":              "test_name",
							"namespace":         "test_namespace",
							"deletionTimestamp": "2010-11-10T23:00:00Z",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"annotations": map[string]interface{}{
								"test_annotation": "test_value",
							},
							"finalizers": []interface{}{
								"finalizer.1",
								"finalizer.2",
							},
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
							"labels": map[string]interface{}{
								"test_label": "test_value",
							},
							"annotations": map[string]interface{}{
								"test_annotation": "test_value",
							},
							"finalizers": []interface{}{
								"finalizer.1",
								"finalizer.2",
							},
						},
					},
				},
			},
			want: false,
		},
		{
			name: "not equal as b has additional fields than a",
			args: args{
				a: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
						},
					},
				},
				b: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"kind":       "test_kind",
						"apiVersion": "test_version",
						"metadata": map[string]interface{}{
							"name":      "test_name",
							"namespace": "test_namespace",
						},
						"other_key": "other_value",
					},
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deepEqualApartFromStatus(tt.args.a, tt.args.b); got != tt.want {
				t.Errorf("deepEqualApartFromStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}

var _ dynamic.ClusterInterface = (*mockedDynamicCluster)(nil)

type mockedDynamicCluster struct {
	client *fake.FakeDynamicClient
}

func (mdc *mockedDynamicCluster) Cluster(name logicalcluster.Name) dynamic.Interface {
	return mdc.client
}

func TestSyncerProcess(t *testing.T) {
	tests := map[string]struct {
		fromNamespace *corev1.Namespace
		gvr           schema.GroupVersionResource
		fromResources []runtime.Object
		toResources   []runtime.Object

		resourceToProcessName               string
		resourceToProcessLogicalClusterName string

		upstreamURL               string
		upstreamLogicalCluster    string
		syncTargetName            string
		syncTargetWorkspace       logicalcluster.Name
		syncTargetUID             types.UID
		advancedSchedulingEnabled bool

		expectError         bool
		expectActionsOnFrom []clienttesting.Action
		expectActionsOnTo   []clienttesting.Action
	}{
		"SpecSyncer sync deployment to downstream, upstream gets patched with the finalizer and the object is not created downstream (will be in the next reconciliation)": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResources: []runtime.Object{
				secret("default-token-abc", "test", "root:org:ws",
					map[string]string{"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync"},
					map[string]string{"kubernetes.io/service-account.name": "default"},
					map[string][]byte{
						"token":     []byte("token"),
						"namespace": []byte("namespace"),
					}),
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				}, nil, nil),
			},
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			syncTargetName:                      "us-west1",

			expectActionsOnFrom: []clienttesting.Action{
				updateDeploymentAction("test",
					toUnstructured(t, changeDeployment(
						deployment("theDeployment", "test", "root:org:ws", map[string]string{
							"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
						}, nil, []string{"workload.kcp.dev/syncer-2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5"}),
					))),
			},
			expectActionsOnTo: []clienttesting.Action{
				createNamespaceAction(
					"",
					changeUnstructured(
						toUnstructured(t, namespace("kcp-hcbsa8z6c2er", "",
							map[string]string{
								"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
							},
							map[string]string{
								"kcp.dev/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
							})),
						removeNilOrEmptyFields,
					),
				),
			},
		},
		"SpecSyncer sync to downstream, syncer finalizer already there": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResources: []runtime.Object{
				secret("default-token-abc", "test", "root:org:ws",
					map[string]string{"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync"},
					map[string]string{"kubernetes.io/service-account.name": "default"},
					map[string][]byte{
						"token":     []byte("token"),
						"namespace": []byte("namespace"),
					}),
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				}, nil, []string{"workload.kcp.dev/syncer-2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5"}),
			},
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			syncTargetName:                      "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				createNamespaceAction(
					"",
					changeUnstructured(
						toUnstructured(t, namespace("kcp-hcbsa8z6c2er", "",
							map[string]string{
								"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
							},
							map[string]string{
								"kcp.dev/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
							})),
						removeNilOrEmptyFields,
					),
				),
				patchDeploymentAction(
					"theDeployment",
					"kcp-hcbsa8z6c2er",
					types.ApplyPatchType,
					toJson(t,
						changeUnstructured(
							toUnstructured(t, deployment("theDeployment", "kcp-hcbsa8z6c2er", "", map[string]string{
								"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
							}, nil, nil)),
							setNestedField(map[string]interface{}{}, "status"),
							setPodSpecServiceAccount("spec", "template", "spec"),
						),
					),
				),
			},
		},
		"SpecSyncer upstream resource has the state workload annotation removed, expect deletion downstream": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResources: []runtime.Object{
				secret("default-token-abc", "test", "root:org:ws",
					map[string]string{"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync"},
					map[string]string{"kubernetes.io/service-account.name": "default"},
					map[string][]byte{
						"token":     []byte("token"),
						"namespace": []byte("namespace"),
					}),
				deployment("theDeployment", "test", "root:org:ws", nil, nil, nil),
			},
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			syncTargetName:                      "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				deleteDeploymentAction(
					"theDeployment",
					"kcp-hcbsa8z6c2er",
				),
			},
		},
		"SpecSyncer deletion: object exist downstream": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			toResources: []runtime.Object{
				namespace("kcp-hcbsa8z6c2er", "", map[string]string{
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				},
					map[string]string{
						"kcp.dev/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
					}),
				deployment("theDeployment", "kcp-hcbsa8z6c2er", "root:org:ws", map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				}, nil, []string{"workload.kcp.dev/syncer-2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5"}),
			},
			fromResources: []runtime.Object{
				secret("default-token-abc", "test", "root:org:ws",
					map[string]string{"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync"},
					map[string]string{"kubernetes.io/service-account.name": "default"},
					map[string][]byte{
						"token":     []byte("token"),
						"namespace": []byte("namespace"),
					}),
				deployment("theDeployment", "test", "root:org:ws",
					map[string]string{"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync"},
					map[string]string{"deletion.internal.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": time.Now().Format(time.RFC3339)},
					[]string{"workload.kcp.dev/syncer-2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5"}),
			},
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			syncTargetName:                      "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				deleteDeploymentAction(
					"theDeployment",
					"kcp-hcbsa8z6c2er",
				),
			},
		},
		"SpecSyncer deletion: object does not exists downstream, upstream finalizer should be removed": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			toResources: []runtime.Object{
				namespace("kcp-hcbsa8z6c2er", "", map[string]string{
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				},
					map[string]string{
						"kcp.dev/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
					}),
			},
			fromResources: []runtime.Object{
				secret("default-token-abc", "test", "root:org:ws",
					map[string]string{"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync"},
					map[string]string{"kubernetes.io/service-account.name": "default"},
					map[string][]byte{
						"token":     []byte("token"),
						"namespace": []byte("namespace"),
					}),
				deployment("theDeployment", "test", "root:org:ws",
					map[string]string{"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync"},
					map[string]string{"another.valid.annotation/this": "value",
						"deletion.internal.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": time.Now().Format(time.RFC3339)},
					[]string{"workload.kcp.dev/syncer-2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5"}),
			},
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			syncTargetName:                      "us-west1",
			expectActionsOnFrom: []clienttesting.Action{
				updateDeploymentAction("test",
					changeUnstructured(
						toUnstructured(t, changeDeployment(
							deployment("theDeployment", "test", "root:org:ws", nil, map[string]string{"another.valid.annotation/this": "value"}, nil),
						),
						),
						// TODO(jmprusi): Those next changes do "nothing", it's just for the test to pass
						//                as the test expects some null fields to be there...
						setNestedField(map[string]interface{}{}, "metadata", "labels"),
						setNestedField([]interface{}{}, "metadata", "finalizers"),
						setNestedField(nil, "spec", "selector"),
					)),
			},
			expectActionsOnTo: []clienttesting.Action{
				deleteDeploymentAction(
					"theDeployment",
					"kcp-hcbsa8z6c2er",
				),
			},
		},
		"SpecSyncer deletion: upstream object has external finalizer, the object shouldn't be deleted": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			toResources: []runtime.Object{
				namespace("kcp-hcbsa8z6c2er", "", map[string]string{
					"internal.workload.kcp.dev/cluster":                             "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				},
					map[string]string{
						"kcp.dev/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
					}),
				deployment("theDeployment", "kcp-hcbsa8z6c2er", "", map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				}, nil, nil),
			},
			fromResources: []runtime.Object{
				secret("default-token-abc", "test", "root:org:ws",
					map[string]string{"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync"},
					map[string]string{"kubernetes.io/service-account.name": "default"},
					map[string][]byte{
						"token":     []byte("token"),
						"namespace": []byte("namespace"),
					}),
				deployment("theDeployment", "test", "root:org:ws",
					map[string]string{"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync"},
					map[string]string{
						"deletion.internal.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": time.Now().Format(time.RFC3339),
						"finalizers.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5":        "another-controller-finalizer",
					},
					[]string{"workload.kcp.dev/syncer-2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5"}),
			},
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			syncTargetName:                      "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				patchDeploymentAction(
					"theDeployment",
					"kcp-hcbsa8z6c2er",
					types.ApplyPatchType,
					toJson(t,
						changeUnstructured(
							toUnstructured(t, deployment("theDeployment", "kcp-hcbsa8z6c2er", "", map[string]string{
								"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
							}, map[string]string{
								"deletion.internal.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": time.Now().Format(time.RFC3339),
								"finalizers.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5":        "another-controller-finalizer",
							}, nil)),
							// TODO(jmprusi): Those next changes do "nothing", it's just for the test to pass
							//                as the test expects some null fields to be there...
							setNestedField(nil, "spec", "selector"),
							setNestedField(map[string]interface{}{}, "spec", "strategy"),
							setNestedField(map[string]interface{}{
								"metadata": map[string]interface{}{
									"creationTimestamp": nil,
								},
								"spec": map[string]interface{}{
									"containers": nil,
								},
							}, "spec", "template"),
							setNestedField(map[string]interface{}{}, "status"),
							setPodSpecServiceAccount("spec", "template", "spec"),
						),
					),
				),
			},
		},
		"SpecSyncer with AdvancedScheduling, sync deployment to downstream and apply SpecDiff": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResources: []runtime.Object{
				secret("default-token-abc", "test", "root:org:ws",
					map[string]string{"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync"},
					map[string]string{"kubernetes.io/service-account.name": "default"},
					map[string][]byte{
						"token":     []byte("token"),
						"namespace": []byte("namespace"),
					}),
				deployment("theDeployment", "test", "root:org:ws",
					map[string]string{
						"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
					},
					map[string]string{"experimental.spec-diff.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "[{\"op\":\"replace\",\"path\":\"/replicas\",\"value\":3}]"},
					[]string{"workload.kcp.dev/syncer-2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5"}),
			},
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			syncTargetName:                      "us-west1",
			advancedSchedulingEnabled:           true,

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				createNamespaceAction(
					"",
					changeUnstructured(
						toUnstructured(t, namespace("kcp-hcbsa8z6c2er", "",
							map[string]string{
								"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
							},
							map[string]string{
								"kcp.dev/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
							})),
						removeNilOrEmptyFields,
					),
				),
				patchDeploymentAction(
					"theDeployment",
					"kcp-hcbsa8z6c2er",
					types.ApplyPatchType,
					toJson(t,
						changeUnstructured(
							toUnstructured(t, deployment("theDeployment", "kcp-hcbsa8z6c2er", "", map[string]string{
								"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
							}, map[string]string{"experimental.spec-diff.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "[{\"op\":\"replace\",\"path\":\"/replicas\",\"value\":3}]"}, nil)),
							setNestedField(map[string]interface{}{
								"replicas": int64(3),
							}, "spec"),
							// TODO(jmprusi): Those next changes do "nothing", it's just for the test to pass
							//                as the test expects some null fields to be there...
							setNestedField(nil, "spec", "selector"),
							setNestedField(map[string]interface{}{}, "spec", "strategy"),
							setNestedField(map[string]interface{}{
								"metadata": map[string]interface{}{
									"creationTimestamp": nil,
								},
								"spec": map[string]interface{}{
									"containers": nil,
								},
							}, "spec", "template"),
							setNestedField(map[string]interface{}{}, "status"),
						),
					),
				),
			},
		},
		"SpecSyncer namespace conflict: try to sync to an already existing namespace with a different namespace-locator, expect error": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResources: []runtime.Object{
				secret("default-token-abc", "test", "root:org:ws",
					map[string]string{"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync"},
					map[string]string{"kubernetes.io/service-account.name": "default"},
					map[string][]byte{
						"token":     []byte("token"),
						"namespace": []byte("namespace"),
					}),
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				}, nil, []string{"workload.kcp.dev/syncer-2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5"}),
			},
			toResources: []runtime.Object{
				namespace("kcp-hcbsa8z6c2er", "", map[string]string{
					"internal.workload.kcp.dev/cluster":                             "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				}, map[string]string{
					"kcp.dev/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"ANOTHERNAMESPACE"}`,
				}),
			},
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			syncTargetName:                      "us-west1",
			expectError:                         true,
			expectActionsOnFrom:                 []clienttesting.Action{},
			expectActionsOnTo:                   []clienttesting.Action{},
		},
		"SpecSyncer namespace conflict: try to sync to an already existing namespace without a namespace-locator, expect error": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResources: []runtime.Object{
				secret("default-token-abc", "test", "root:org:ws",
					map[string]string{"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync"},
					map[string]string{"kubernetes.io/service-account.name": "default"},
					map[string][]byte{
						"token":     []byte("token"),
						"namespace": []byte("namespace"),
					}),
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				}, nil, []string{"workload.kcp.dev/syncer-2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5"}),
			},
			toResources: []runtime.Object{
				namespace("kcp-hcbsa8z6c2er", "", map[string]string{
					"internal.workload.kcp.dev/cluster":                             "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				}, map[string]string{},
				),
			},
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			syncTargetName:                      "us-west1",
			expectError:                         true,
			expectActionsOnFrom:                 []clienttesting.Action{},
			expectActionsOnTo:                   []clienttesting.Action{},
		},
		"old v0.6.0 namespace locator exists downstream": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"},
			toResources: []runtime.Object{
				namespace("kcp-01c0zzvlqsi7n", "", map[string]string{
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				},
					map[string]string{
						"kcp.dev/namespace-locator": `{"syncTarget":{"path":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
					}),
				secret("foo", "test", "root:org:ws",
					map[string]string{"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync"},
					nil,
					map[string][]byte{
						"a": []byte("b"),
					}),
			},
			fromResources: []runtime.Object{
				secretWithFinalizers("foo", "test", "root:org:ws",
					map[string]string{
						"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
						"something": "else"},
					nil,
					[]string{"workload.kcp.dev/syncer-2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5"},
					map[string][]byte{
						"a": []byte("b"),
					}),
			},
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "foo",
			syncTargetName:                      "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				patchSecretAction(
					"foo",
					"kcp-01c0zzvlqsi7n",
					types.ApplyPatchType,
					[]byte(`{"apiVersion":"v1","data":{"a":"Yg=="},"kind":"Secret","metadata":{"creationTimestamp":null,"labels":{"internal.workload.kcp.dev/cluster":"2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5","something":"else"},"name":"foo","namespace":"kcp-01c0zzvlqsi7n"},"type":"kubernetes.io/service-account-token"}`),
				),
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			kcpLogicalCluster := logicalcluster.New(tc.upstreamLogicalCluster)
			syncTargetUID := tc.syncTargetUID
			if tc.syncTargetUID == "" {
				syncTargetUID = types.UID("syncTargetUID")
			}

			if tc.syncTargetWorkspace.Empty() {
				tc.syncTargetWorkspace = logicalcluster.New("root:org:ws")
			}

			var allFromResources []runtime.Object
			allFromResources = append(allFromResources, tc.fromNamespace)
			if tc.fromResources != nil {
				allFromResources = append(allFromResources, tc.fromResources...)
			}

			fromClient := dynamicfake.NewSimpleDynamicClient(scheme, allFromResources...)
			fromClusterClient := &mockedDynamicCluster{
				client: fromClient,
			}

			syncTargetKey := workloadv1alpha1.ToSyncTargetKey(tc.syncTargetWorkspace, tc.syncTargetName)

			toClient := dynamicfake.NewSimpleDynamicClient(scheme, tc.toResources...)
			fromInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(fromClusterClient.Cluster(logicalcluster.Wildcard), time.Hour, metav1.NamespaceAll, func(o *metav1.ListOptions) {
				o.LabelSelector = workloadv1alpha1.ClusterResourceStateLabelPrefix + syncTargetKey + "=" + string(workloadv1alpha1.ResourceStateSync)
			})
			toInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(toClient, time.Hour, metav1.NamespaceAll, func(o *metav1.ListOptions) {
				o.LabelSelector = workloadv1alpha1.ClusterResourceStateLabelPrefix + syncTargetKey + "=" + string(workloadv1alpha1.ResourceStateSync)
			})

			setupServersideApplyPatchReactor(toClient)
			namespaceWatcherStarted := setupWatchReactor("namespaces", fromClient)
			resourceWatcherStarted := setupWatchReactor(tc.gvr.Resource, fromClient)

			gvrs := []schema.GroupVersionResource{
				{Group: "", Version: "v1", Resource: "namespaces"},
				{Group: "", Version: "v1", Resource: "secrets"},
				tc.gvr,
			}
			upstreamURL, err := url.Parse("https://kcp.dev:6443")
			require.NoError(t, err)
			controller, err := NewSpecSyncer(gvrs, kcpLogicalCluster, tc.syncTargetName, syncTargetKey, upstreamURL, tc.advancedSchedulingEnabled, fromClusterClient, toClient, fromInformers, toInformers, syncTargetUID)
			require.NoError(t, err)

			fromInformers.Start(ctx.Done())
			toInformers.Start(ctx.Done())

			fromInformers.WaitForCacheSync(ctx.Done())
			toInformers.WaitForCacheSync(ctx.Done())

			<-resourceWatcherStarted
			<-namespaceWatcherStarted

			fromClient.ClearActions()
			toClient.ClearActions()

			key := tc.fromNamespace.Name + "/" + clusters.ToClusterAwareKey(logicalcluster.New(tc.resourceToProcessLogicalClusterName), tc.resourceToProcessName)
			err = controller.process(context.Background(),
				tc.gvr,
				key,
			)
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.EqualValues(t, tc.expectActionsOnFrom, fromClient.Actions())
			assert.EqualValues(t, tc.expectActionsOnTo, toClient.Actions())
		})
	}
}

func setupServersideApplyPatchReactor(toClient *dynamicfake.FakeDynamicClient) {
	toClient.PrependReactor("patch", "*", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
		patchAction := action.(clienttesting.PatchAction)
		if patchAction.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil
		}
		return true, nil, err
	})
}

func setupWatchReactor(resource string, fromClient *dynamicfake.FakeDynamicClient) chan struct{} {
	watcherStarted := make(chan struct{})
	fromClient.PrependWatchReactor(resource, func(action clienttesting.Action) (handled bool, ret watch.Interface, err error) {
		gvr := action.GetResource()
		ns := action.GetNamespace()
		watch, err := fromClient.Tracker().Watch(gvr, ns)
		if err != nil {
			return false, nil, err
		}
		close(watcherStarted)
		return true, watch, nil
	})
	return watcherStarted
}

func namespace(name, clusterName string, labels, annotations map[string]string) *corev1.Namespace {
	if clusterName != "" {
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[logicalcluster.AnnotationKey] = clusterName
	}

	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: annotations,
		},
	}
}

func deployment(name, namespace, clusterName string, labels, annotations map[string]string, finalizers []string) *appsv1.Deployment {
	if clusterName != "" {
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[logicalcluster.AnnotationKey] = clusterName
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
			Finalizers:  finalizers,
		},
	}
}

func secret(name, namespace, clusterName string, labels, annotations map[string]string, data map[string][]byte) *corev1.Secret {
	return secretWithFinalizers(name, namespace, clusterName, labels, annotations, nil, data)
}

func secretWithFinalizers(name, namespace, clusterName string, labels, annotations map[string]string, finalizers []string, data map[string][]byte) *corev1.Secret {
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[logicalcluster.AnnotationKey] = clusterName

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
			Finalizers:  finalizers,
		},
		Data:       data,
		StringData: nil,
		Type:       corev1.SecretTypeServiceAccountToken,
	}
}

type deploymentChange func(*appsv1.Deployment)

func changeDeployment(in *appsv1.Deployment, changes ...deploymentChange) *appsv1.Deployment {
	for _, change := range changes {
		change(in)
	}
	return in
}

func toJson(t require.TestingT, object runtime.Object) []byte {
	result, err := json.Marshal(object)
	require.NoError(t, err)
	return result
}

func toUnstructured(t require.TestingT, obj metav1.Object) *unstructured.Unstructured {
	var result unstructured.Unstructured
	err := scheme.Convert(obj, &result, nil)
	require.NoError(t, err)

	return &result
}

type unstructuredChange func(d *unstructured.Unstructured)

func changeUnstructured(in *unstructured.Unstructured, changes ...unstructuredChange) *unstructured.Unstructured {
	for _, change := range changes {
		change(in)
	}
	return in
}

func removeNilOrEmptyFields(in *unstructured.Unstructured) {
	if val, exists, _ := unstructured.NestedFieldNoCopy(in.UnstructuredContent(), "metadata", "creationTimestamp"); val == nil && exists {
		unstructured.RemoveNestedField(in.UnstructuredContent(), "metadata", "creationTimestamp")
	}
	if val, exists, _ := unstructured.NestedMap(in.UnstructuredContent(), "spec"); len(val) == 0 && exists {
		delete(in.Object, "spec")
	}
	if val, exists, _ := unstructured.NestedMap(in.UnstructuredContent(), "status"); len(val) == 0 && exists {
		delete(in.Object, "status")
	}
}

func setNestedField(value interface{}, fields ...string) unstructuredChange {
	return func(d *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(d.UnstructuredContent(), value, fields...)
	}
}

func setPodSpecServiceAccount(fields ...string) unstructuredChange {
	var j interface{}
	err := json.Unmarshal([]byte(`{
	"automountServiceAccountToken":false,
	"containers":null,
	"volumes":[
		{"name":"kcp-api-access","projected":{
			"defaultMode":420,
			"sources":[
				{"secret":{"items":[{"key":"token","path":"token"},{"key":"namespace","path":"namespace"}],"name": "kcp-default-token-abc"}},
				{"configMap":{"items":[{"key":"ca.crt","path":"ca.crt"}],"name":"kcp-root-ca.crt"}}
			]
		}}
	]
}`), &j)
	if err != nil {
		panic(err)
	}
	return setNestedField(j, fields...)
}

func deploymentAction(verb, namespace string, subresources ...string) clienttesting.ActionImpl {
	return clienttesting.ActionImpl{
		Namespace:   namespace,
		Verb:        verb,
		Resource:    schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		Subresource: strings.Join(subresources, "/"),
	}
}

func namespaceAction(verb string, subresources ...string) clienttesting.ActionImpl {
	return clienttesting.ActionImpl{
		Namespace:   "",
		Verb:        verb,
		Resource:    schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"},
		Subresource: strings.Join(subresources, "/"),
	}
}

func createNamespaceAction(name string, object runtime.Object) clienttesting.CreateActionImpl {
	return clienttesting.CreateActionImpl{
		ActionImpl: namespaceAction("create"),
		Name:       name,
		Object:     object,
	}
}

func updateDeploymentAction(namespace string, object runtime.Object, subresources ...string) clienttesting.UpdateActionImpl {
	return clienttesting.UpdateActionImpl{
		ActionImpl: deploymentAction("update", namespace, subresources...),
		Object:     object,
	}
}

func patchDeploymentAction(name, namespace string, patchType types.PatchType, patch []byte, subresources ...string) clienttesting.PatchActionImpl {
	return clienttesting.PatchActionImpl{
		ActionImpl: deploymentAction("patch", namespace, subresources...),
		Name:       name,
		PatchType:  patchType,
		Patch:      patch,
	}
}

func deleteDeploymentAction(name, namespace string, subresources ...string) clienttesting.DeleteActionImpl {
	return clienttesting.DeleteActionImpl{
		ActionImpl:    deploymentAction("delete", namespace, subresources...),
		Name:          name,
		DeleteOptions: metav1.DeleteOptions{},
	}
}

func secretAction(verb, namespace string, subresources ...string) clienttesting.ActionImpl {
	return clienttesting.ActionImpl{
		Namespace:   namespace,
		Verb:        verb,
		Resource:    schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"},
		Subresource: strings.Join(subresources, "/"),
	}
}

func patchSecretAction(name, namespace string, patchType types.PatchType, patch []byte, subresources ...string) clienttesting.PatchActionImpl {
	return clienttesting.PatchActionImpl{
		ActionImpl: secretAction("patch", namespace, subresources...),
		Name:       name,
		PatchType:  patchType,
		Patch:      patch,
	}
}
