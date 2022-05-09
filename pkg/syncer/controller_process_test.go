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

package syncer

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster"
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
	"k8s.io/client-go/dynamic/dynamicinformer"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

var scheme *runtime.Scheme

func init() {
	scheme = runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
}

func TestSyncerProcess(t *testing.T) {
	tests := map[string]struct {
		fromNamespace *corev1.Namespace
		gvr           schema.GroupVersionResource
		fromResource  runtime.Object
		toResources   []runtime.Object

		resourceToProcessName               string
		resourceToProcessLogicalClusterName string

		direction                 SyncDirection
		upstreamURL               string
		upstreamLogicalCluster    string
		workloadClusterName       string
		advancedSchedulingEnabled bool

		expectError         bool
		expectActionsOnFrom []clienttesting.Action
		expectActionsOnTo   []clienttesting.Action
	}{
		"SpecSyncer upsert": {
			direction:              SyncDown,
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"state.internal.workloads.kcp.dev/us-west1": "Sync",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: deployment("theDeployment", "test", "root:org:ws", map[string]string{
				"state.internal.workloads.kcp.dev/us-west1": "Sync",
			}, nil, nil),
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			workloadClusterName:                 "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				createNamespaceAction(
					"",
					changeUnstructured(
						toUnstructured(t, namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
							map[string]string{
								"state.internal.workloads.kcp.dev/us-west1": "Sync",
							},
							map[string]string{
								"kcp.dev/namespace-locator": `{"logical-cluster":"root:org:ws","namespace":"test"}`,
							})),
						removeNilOrEmptyFields,
					),
				),
				patchDeploymentAction(
					"theDeployment",
					"kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973",
					types.ApplyPatchType,
					toJson(t,
						changeUnstructured(
							toUnstructured(t, deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
								"state.internal.workloads.kcp.dev/us-west1": "Sync",
							}, nil, nil)),
							setNestedField(map[string]interface{}{}, "status"),
							setPodSpecServiceAccount("spec", "template", "spec"),
						),
					),
				),
			},
		},
		"SpecSyncer upstream deletion": {
			direction:              SyncDown,
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"state.internal.workloads.kcp.dev/us-west1": "Sync",
			}, nil),
			gvr:                                 schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource:                        deployment("theDeployment", "test", "root:org:ws", nil, nil, nil),
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			workloadClusterName:                 "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				deleteDeploymentAction(
					"theDeployment",
					"kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973",
				),
			},
		},
		"StatusSyncer upsert to existing resource": {
			direction:              SyncUp,
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"logical-cluster":"root:org:ws","namespace":"test"}`,
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				}, nil, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				}, nil, nil),
			},
			resourceToProcessLogicalClusterName: "",
			resourceToProcessName:               "theDeployment",
			workloadClusterName:                 "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				getDeploymentAction("theDeployment", "test"),
				updateDeploymentAction("test",
					toUnstructured(t, changeDeployment(
						deployment("theDeployment", "test", "", map[string]string{
							"state.internal.workloads.kcp.dev/us-west1": "Sync",
						}, nil, nil),
						addDeploymentStatus(appsv1.DeploymentStatus{
							Replicas: 15,
						}))),
					"status"),
			},
		},
		"StatusSyncer upstream deletion": {
			direction:              SyncUp,
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"logical-cluster":"root:org:ws","namespace":"test"}`,
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", nil, nil, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				}, nil, nil),
			},
			resourceToProcessLogicalClusterName: "",
			resourceToProcessName:               "theDeployment",
			workloadClusterName:                 "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo:   []clienttesting.Action{},
		},
		"SpecSyncer with AdvancedScheduling, sync downstream deployment": {
			direction:              SyncDown,
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"state.internal.workloads.kcp.dev/us-west1": "Sync",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: deployment("theDeployment", "test", "root:org:ws", map[string]string{
				"state.internal.workloads.kcp.dev/us-west1": "Sync",
			}, nil, nil),
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			workloadClusterName:                 "us-west1",
			advancedSchedulingEnabled:           true,

			expectActionsOnFrom: []clienttesting.Action{
				updateDeploymentAction("test",
					toUnstructured(t, changeDeployment(
						deployment("theDeployment", "test", "root:org:ws", map[string]string{
							"state.internal.workloads.kcp.dev/us-west1": "Sync",
						}, nil, []string{"workloads.kcp.dev/syncer-us-west1"}),
					))),
			},
			expectActionsOnTo: []clienttesting.Action{
				createNamespaceAction(
					"",
					changeUnstructured(
						toUnstructured(t, namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
							map[string]string{
								"state.internal.workloads.kcp.dev/us-west1": "Sync",
							},
							map[string]string{
								"kcp.dev/namespace-locator": `{"logical-cluster":"root:org:ws","namespace":"test"}`,
							})),
						removeNilOrEmptyFields,
					),
				),
				patchDeploymentAction(
					"theDeployment",
					"kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973",
					types.ApplyPatchType,
					toJson(t,
						changeUnstructured(
							toUnstructured(t, deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
								"state.internal.workloads.kcp.dev/us-west1": "Sync",
							}, nil, nil)),
							setNestedField(map[string]interface{}{}, "status"),
							setPodSpecServiceAccount("spec", "template", "spec"),
						),
					),
				),
			},
		},
		"SpecSyncer with AdvancedScheduling, sync downstream deployment and apply SpecDiff": {
			direction:              SyncDown,
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"state.internal.workloads.kcp.dev/us-west1": "Sync",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: deployment("theDeployment", "test", "root:org:ws", map[string]string{
				"state.internal.workloads.kcp.dev/us-west1": "Sync",
			}, map[string]string{"experimental.spec-diff.workloads.kcp.dev/us-west1": "[{\"op\":\"replace\",\"path\":\"/replicas\",\"value\":3}]"}, nil),
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			workloadClusterName:                 "us-west1",
			advancedSchedulingEnabled:           true,

			expectActionsOnFrom: []clienttesting.Action{
				updateDeploymentAction("test",
					toUnstructured(t, changeDeployment(
						deployment("theDeployment", "test", "root:org:ws", map[string]string{
							"state.internal.workloads.kcp.dev/us-west1": "Sync",
						}, map[string]string{"experimental.spec-diff.workloads.kcp.dev/us-west1": "[{\"op\":\"replace\",\"path\":\"/replicas\",\"value\":3}]"}, []string{syncerFinalizerNamePrefix + "us-west1"}),
					))),
			},
			expectActionsOnTo: []clienttesting.Action{
				createNamespaceAction(
					"",
					changeUnstructured(
						toUnstructured(t, namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
							map[string]string{
								"state.internal.workloads.kcp.dev/us-west1": "Sync",
							},
							map[string]string{
								"kcp.dev/namespace-locator": `{"logical-cluster":"root:org:ws","namespace":"test"}`,
							})),
						removeNilOrEmptyFields,
					),
				),
				patchDeploymentAction(
					"theDeployment",
					"kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973",
					types.ApplyPatchType,
					toJson(t,
						changeUnstructured(
							toUnstructured(t, deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
								"state.internal.workloads.kcp.dev/us-west1": "Sync",
							}, map[string]string{"experimental.spec-diff.workloads.kcp.dev/us-west1": "[{\"op\":\"replace\",\"path\":\"/replicas\",\"value\":3}]"}, nil)),
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
		"SpecSyncer with AdvancedScheduling, deletion: object exist downstream": {
			direction:              SyncDown,
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"state.internal.workloads.kcp.dev/us-west1": "Sync",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			toResources: []runtime.Object{
				namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				},
					map[string]string{
						"kcp.dev/namespace-locator": `{"logical-cluster":"root:org:ws","namespace":"test"}`,
					}),
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "root:org:ws", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				}, nil, []string{"workloads.kcp.dev/syncer-us-west1"}),
			},
			fromResource: deployment("theDeployment", "test", "root:org:ws",
				map[string]string{"state.internal.workloads.kcp.dev/us-west1": "Sync"},
				map[string]string{"deletion.internal.workloads.kcp.dev/us-west1": time.Now().Format(time.RFC3339)},
				[]string{"workloads.kcp.dev/syncer-us-west1"}),
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			workloadClusterName:                 "us-west1",
			advancedSchedulingEnabled:           true,

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				createNamespaceAction(
					"",
					changeUnstructured(
						toUnstructured(t, namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
							map[string]string{
								"state.internal.workloads.kcp.dev/us-west1": "Sync",
							},
							map[string]string{
								"kcp.dev/namespace-locator": `{"logical-cluster":"root:org:ws","namespace":"test"}`,
							})),
						removeNilOrEmptyFields,
					),
				),
				deleteDeploymentAction(
					"theDeployment",
					"kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973",
				),
			},
		},
		"SpecSyncer with AdvancedScheduling, deletion: object does not exists downstream": {
			direction:              SyncDown,
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"state.internal.workloads.kcp.dev/us-west1": "Sync",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			toResources: []runtime.Object{
				namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				},
					map[string]string{
						"kcp.dev/namespace-locator": `{"logical-cluster":"root:org:ws","namespace":"test"}`,
					}),
			},
			fromResource: deployment("theDeployment", "test", "root:org:ws",
				map[string]string{"state.internal.workloads.kcp.dev/us-west1": "Sync"},
				map[string]string{"another.valid.annotation/this": "value",
					"deletion.internal.workloads.kcp.dev/us-west1": time.Now().Format(time.RFC3339)},
				[]string{"workloads.kcp.dev/syncer-us-west1"}),
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			workloadClusterName:                 "us-west1",
			advancedSchedulingEnabled:           true,

			expectActionsOnFrom: []clienttesting.Action{
				getDeploymentAction("theDeployment", "test"),
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
				createNamespaceAction(
					"",
					changeUnstructured(
						toUnstructured(t, namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
							map[string]string{
								"state.internal.workloads.kcp.dev/us-west1": "Sync",
							},
							map[string]string{
								"kcp.dev/namespace-locator": `{"logical-cluster":"root:org:ws","namespace":"test"}`,
							})),
						removeNilOrEmptyFields,
					),
				),
				deleteDeploymentAction(
					"theDeployment",
					"kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973",
				),
			},
		},
		"SpecSyncer with AdvancedScheduling, deletion: upstream object has external finalizer": {
			direction:              SyncDown,
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "root:org:ws", map[string]string{
				"state.internal.workloads.kcp.dev/us-west1": "Sync",
			}, nil),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			toResources: []runtime.Object{
				namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				},
					map[string]string{
						"kcp.dev/namespace-locator": `{"logical-cluster":"root:org:ws","namespace":"test"}`,
					}),
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				}, nil, []string{"workloads.kcp.dev/syncer-us-west1"}),
			},
			fromResource: deployment("theDeployment", "test", "root:org:ws",
				map[string]string{"state.internal.workloads.kcp.dev/us-west1": "Sync"},
				map[string]string{
					"deletion.internal.workloads.kcp.dev/us-west1": time.Now().Format(time.RFC3339),
					"finalizers.workloads.kcp.dev/us-west1":        "another-controller-finalizer",
				},
				[]string{"workloads.kcp.dev/syncer-us-west1"}),
			resourceToProcessLogicalClusterName: "root:org:ws",
			resourceToProcessName:               "theDeployment",
			workloadClusterName:                 "us-west1",
			advancedSchedulingEnabled:           true,

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				createNamespaceAction(
					"",
					changeUnstructured(
						toUnstructured(t, namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
							map[string]string{
								"state.internal.workloads.kcp.dev/us-west1": "Sync",
							},
							map[string]string{
								"kcp.dev/namespace-locator": `{"logical-cluster":"root:org:ws","namespace":"test"}`,
							})),
						removeNilOrEmptyFields,
					),
				),
				patchDeploymentAction(
					"theDeployment",
					"kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973",
					types.ApplyPatchType,
					toJson(t,
						changeUnstructured(
							toUnstructured(t, deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
								"state.internal.workloads.kcp.dev/us-west1": "Sync",
							}, map[string]string{
								"deletion.internal.workloads.kcp.dev/us-west1": time.Now().Format(time.RFC3339),
								"finalizers.workloads.kcp.dev/us-west1":        "another-controller-finalizer",
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
		"StatusSyncer with AdvancedScheduling, update status upstream": {
			direction:              SyncUp,
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"logical-cluster":"root:org:ws","namespace":"test"}`,
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				}, nil, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				}, nil, nil),
			},
			resourceToProcessLogicalClusterName: "",
			resourceToProcessName:               "theDeployment",
			workloadClusterName:                 "us-west1",
			advancedSchedulingEnabled:           true,

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				getDeploymentAction("theDeployment", "test"),
				updateDeploymentAction("test",
					toUnstructured(t, changeDeployment(
						deployment("theDeployment", "test", "root:org:ws", map[string]string{
							"state.internal.workloads.kcp.dev/us-west1": "Sync",
						}, map[string]string{
							"experimental.status.workloads.kcp.dev/us-west1": "{\"replicas\":15}",
						}, nil)))),
			},
		},
		"StatusSyncer with AdvancedScheduling, deletion: object exists upstream": {
			direction:              SyncUp,
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"logical-cluster":"root:org:ws","namespace":"test"}`,
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				}, nil, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				}, map[string]string{
					"deletion.internal.workloads.kcp.dev/us-west1":   time.Now().Format(time.RFC3339),
					"experimental.status.workloads.kcp.dev/us-west1": "{\"replicas\":15}",
				}, []string{"workloads.kcp.dev/syncer-us-west1"}),
			},
			resourceToProcessLogicalClusterName: "",
			resourceToProcessName:               "theDeployment",
			workloadClusterName:                 "us-west1",
			advancedSchedulingEnabled:           true,

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				getDeploymentAction("theDeployment", "test"),
			},
		},
		"StatusSyncer with AdvancedScheduling, deletion: object does not exists upstream": {
			direction:              SyncUp,
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"logical-cluster":"root:org:ws","namespace":"test"}`,
				}),
			gvr:          schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: nil,
			toResources: []runtime.Object{
				namespace("test", "root:org:ws", map[string]string{"state.internal.workloads.kcp.dev/us-west1": "Sync"}, nil),
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.internal.workloads.kcp.dev/us-west1": "Sync",
				}, map[string]string{
					"deletion.internal.workloads.kcp.dev/us-west1":   time.Now().Format(time.RFC3339),
					"experimental.status.workloads.kcp.dev/us-west1": `{"replicas":15}`,
				}, []string{"workloads.kcp.dev/syncer-us-west1"}),
			},
			resourceToProcessLogicalClusterName: "",
			resourceToProcessName:               "theDeployment",
			workloadClusterName:                 "us-west1",
			advancedSchedulingEnabled:           true,

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
				getDeploymentAction("theDeployment", "test"),
				updateDeploymentAction("test",
					changeUnstructured(
						toUnstructured(t, changeDeployment(
							deployment("theDeployment", "test", "root:org:ws", map[string]string{}, map[string]string{}, nil))),
						// TODO(jmprusi): Those next changes do "nothing", it's just for the test to pass
						//                as the test expects some null fields to be there...
						setNestedField(map[string]interface{}{}, "metadata", "annotations"),
						setNestedField(map[string]interface{}{}, "metadata", "labels"),
						setNestedField([]interface{}{}, "metadata", "finalizers"),
						setNestedField(nil, "spec", "selector"),
					),
				),
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			kcpLogicalCluster := logicalcluster.New(tc.upstreamLogicalCluster)
			var allFromResources []runtime.Object
			allFromResources = append(allFromResources, tc.fromNamespace)
			if tc.fromResource != nil {
				allFromResources = append(allFromResources, tc.fromResource)
			}
			fromClient := dynamicfake.NewSimpleDynamicClient(scheme, allFromResources...)
			toClient := dynamicfake.NewSimpleDynamicClient(scheme, tc.toResources...)

			fromInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(fromClient, resyncPeriod, metav1.NamespaceAll, func(o *metav1.ListOptions) {
				o.LabelSelector = workloadv1alpha1.InternalClusterResourceStateLabelPrefix + tc.workloadClusterName + "=" + string(workloadv1alpha1.ResourceStateSync)
			})
			toInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(toClient, resyncPeriod, metav1.NamespaceAll, func(o *metav1.ListOptions) {
				o.LabelSelector = workloadv1alpha1.InternalClusterResourceStateLabelPrefix + tc.workloadClusterName + "=" + string(workloadv1alpha1.ResourceStateSync)
			})

			setupServersideApplyPatchReactor(toClient)
			namespaceWatcherStarted := setupWatchReactor("namespaces", fromClient)
			resourceWatcherStarted := setupWatchReactor(tc.gvr.Resource, fromClient)

			gvrs := []schema.GroupVersionResource{
				{Group: "", Version: "v1", Resource: "namespaces"},
				tc.gvr,
			}
			var controller *Controller
			if tc.direction == SyncUp {
				s, err := NewStatusSyncer(gvrs, kcpLogicalCluster, tc.workloadClusterName, tc.advancedSchedulingEnabled, toClient, fromClient, toInformers, fromInformers)
				require.NoError(t, err)
				controller = s.Controller
			} else {
				upstreamURL, err := url.Parse("https://kcp.dev:6443")
				require.NoError(t, err)
				s, err := NewSpecSyncer(gvrs, kcpLogicalCluster, tc.workloadClusterName, upstreamURL, tc.advancedSchedulingEnabled, fromClient, toClient, fromInformers, toInformers)
				require.NoError(t, err)
				controller = s.Controller
			}

			fromInformers.Start(ctx.Done())
			toInformers.Start(ctx.Done())

			fromInformers.WaitForCacheSync(ctx.Done())
			toInformers.WaitForCacheSync(ctx.Done())

			<-resourceWatcherStarted
			<-namespaceWatcherStarted

			fromClient.ClearActions()
			toClient.ClearActions()

			err := controller.process(context.Background(), holder{
				gvr: schema.GroupVersionResource{
					Group:    "apps",
					Version:  "v1",
					Resource: "deployments",
				},
				clusterName: logicalcluster.New(tc.resourceToProcessLogicalClusterName),
				namespace:   tc.fromNamespace.Name,
				name:        tc.resourceToProcessName,
			})
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
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			ClusterName: clusterName,
			Labels:      labels,
			Annotations: annotations,
		},
	}
}

func deployment(name, namespace, clusterName string, labels, annotations map[string]string, finalizers []string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			ClusterName: clusterName,
			Labels:      labels,
			Annotations: annotations,
			Finalizers:  finalizers,
		},
	}
}

type deploymentChange func(*appsv1.Deployment)

func changeDeployment(in *appsv1.Deployment, changes ...deploymentChange) *appsv1.Deployment {
	for _, change := range changes {
		change(in)
	}
	return in
}

func addDeploymentStatus(status appsv1.DeploymentStatus) deploymentChange {
	return func(d *appsv1.Deployment) {
		d.Status = status
	}
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
	"serviceAccountName":"kcp-default",
	"volumes":[
		{"name":"kcp-api-access","projected":{
			"defaultMode":420,
			"sources":[
				{"secret":{"items":[{"key":"token","path":"token"}],"name":"kcp-default-token"}},
				{"configMap":{"items":[{"key":"ca.crt","path":"ca.crt"}],"name":"kcp-root-ca.crt"}},
				{"downwardAPI":{
					"items":[{"fieldRef":{"apiVersion":"v1","fieldPath":"metadata.namespace"},"path":"namespace"}]
				}}
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

func getDeploymentAction(name, namespace string) clienttesting.GetActionImpl {
	return clienttesting.GetActionImpl{
		ActionImpl: deploymentAction("get", namespace),
		Name:       name,
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
