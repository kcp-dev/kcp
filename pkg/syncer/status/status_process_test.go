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

package status

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpdynamicinformer "github.com/kcp-dev/client-go/dynamic/dynamicinformer"
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kcpfakedynamic "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/dynamic/fake"
	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
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
	"k8s.io/client-go/informers"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/syncer/resourcesync"
)

var scheme *runtime.Scheme

func init() {
	scheme = runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
}

func TestDeepEqualFinalizersAndStatus(t *testing.T) {
	for _, c := range []struct {
		desc     string
		old, new *unstructured.Unstructured
		want     bool
	}{{
		desc: "both objects have same status",
		old: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"status": map[string]string{
					"cool": "yes",
				},
			},
		},
		new: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"status": map[string]string{
					"cool": "yes",
				},
			},
		},
		want: true,
	}, {
		desc: "both objects have status; different",
		old: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"status": map[string]string{
					"cool": "yes",
				},
			},
		},
		new: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"status": map[string]string{
					"cool": "no",
				},
			},
		},
		want: false,
	}, {
		desc: "one object doesn't have status",
		old: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"status": map[string]string{
					"cool": "yes",
				},
			},
		},
		new: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"spec": map[string]string{},
			},
		},
		want: false,
	}, {
		desc: "both objects don't have status",
		old: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"spec": map[string]string{},
			},
		},
		new: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"spec": map[string]string{},
			},
		},
		want: true,
	}, {
		desc: "both objects have the same finalizers",
		old: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"finalizers": []interface{}{
						"finalizer.1",
						"finalizer.2",
					},
				},
			},
		},
		new: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"finalizers": []interface{}{
						"finalizer.1",
						"finalizer.2",
					},
				},
			},
		},
		want: true,
	}, {
		desc: "one object doesn't have finalizers",
		old: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"finalizers": []interface{}{
						"finalizer.1",
						"finalizer.2",
					},
				},
			},
		},
		new: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{},
			},
		},
		want: false,
	}, {
		desc: "both objects don't have finalizers",
		old: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{},
			},
		},
		new: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{},
			},
		},
		want: true,
	}, {
		desc: "objects have different finalizers",
		old: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"finalizers": []interface{}{
						"finalizer.1",
						"finalizer.2",
					},
				},
			},
		},
		new: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"finalizers": []interface{}{
						"finalizer.2",
						"finalizer.3",
					},
				},
			},
		},
		want: false,
	}, {
		desc: "one object doesn't have finalizers",
		old: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"finalizers": []interface{}{
						"finalizer.1",
						"finalizer.2",
					},
				},
			},
		},
		new: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{},
			},
		},
		want: false,
	}, {
		desc: "objects have the same status and finalizers but different labels",
		old: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]interface{}{
						"cool": "yes",
					},
					"finalizers": []interface{}{
						"finalizer.1",
						"finalizer.2",
					},
				},
				"status": map[string]string{
					"cool": "yes",
				},
			},
		},
		new: &unstructured.Unstructured{
			Object: map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels": map[string]interface{}{
						"cool": "no!",
					},
					"finalizers": []interface{}{
						"finalizer.1",
						"finalizer.2",
					},
				},
				"status": map[string]string{
					"cool": "yes",
				},
			},
		},
		want: true,
	},
		{
			desc: "objects have equal finalizers and statuses",
			old: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"finalizers": []interface{}{
							"finalizer.1",
							"finalizer.2",
						},
					},
					"status": map[string]string{
						"cool": "yes",
					},
				},
			},
			new: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"finalizers": []interface{}{
							"finalizer.1",
							"finalizer.2",
						},
					},
					"status": map[string]string{
						"cool": "yes",
					},
				},
			},
			want: true,
		}} {
		t.Run(c.desc, func(t *testing.T) {
			got := deepEqualFinalizersAndStatus(c.old, c.new)
			if got != c.want {
				t.Fatalf("got %t, want %t", got, c.want)
			}
		})
	}
}

func TestSyncerProcess(t *testing.T) {
	tests := map[string]struct {
		fromNamespace *corev1.Namespace
		gvr           schema.GroupVersionResource
		fromResource  runtime.Object
		toResources   []runtime.Object

		resourceToProcessName string

		upstreamURL               string
		upstreamLogicalCluster    logicalcluster.Name
		syncTargetName            string
		syncTargetClusterName     logicalcluster.Name
		syncTargetUID             types.UID
		advancedSchedulingEnabled bool

		expectError         bool
		expectActionsOnFrom []clienttesting.Action
		expectActionsOnTo   []kcptesting.Action
	}{
		"StatusSyncer upsert to existing resource": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				}, nil, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				}, nil, nil),
			},
			resourceToProcessName: "theDeployment",
			syncTargetName:        "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []kcptesting.Action{
				updateDeploymentAction("test",
					toUnstructured(t, changeDeployment(
						deployment("theDeployment", "test", "root:org:ws", map[string]string{
							"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
						}, nil, nil),
						addDeploymentStatus(appsv1.DeploymentStatus{
							Replicas: 15,
						}))),
					"status"),
			},
		},
		"StatusSyncer upsert to existing resource but owned by another synctarget, expect no update": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"ANOTHERSYNCTARGETUID"},"workspace":"root:org:ws","namespace":"test"}`,
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				}, nil, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				}, nil, nil),
			},
			resourceToProcessName: "theDeployment",
			syncTargetName:        "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo:   []kcptesting.Action{},
		},
		"StatusSyncer upstream deletion": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", nil, nil, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				}, nil, nil),
			},
			resourceToProcessName: "theDeployment",
			syncTargetName:        "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo:   []kcptesting.Action{},
		},
		"StatusSyncer with AdvancedScheduling, update status upstream": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				}, nil, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				}, nil, nil),
			},
			resourceToProcessName:     "theDeployment",
			syncTargetName:            "us-west1",
			advancedSchedulingEnabled: true,

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []kcptesting.Action{
				updateDeploymentAction("test",
					toUnstructured(t, changeDeployment(
						deployment("theDeployment", "test", "root:org:ws", map[string]string{
							"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
						}, map[string]string{
							"experimental.status.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "{\"replicas\":15}",
						}, nil)))),
			},
		},
		"StatusSyncer with AdvancedScheduling, deletion: object exists upstream": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				}, nil, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				}, map[string]string{
					"deletion.internal.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5":   time.Now().Format(time.RFC3339),
					"experimental.status.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "{\"replicas\":15}",
				}, []string{"workload.kcp.dev/syncer-2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5"}),
			},
			resourceToProcessName:     "theDeployment",
			syncTargetName:            "us-west1",
			advancedSchedulingEnabled: true,

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo:   []kcptesting.Action{},
		},
		"StatusSyncer with AdvancedScheduling, deletion: object does not exists upstream": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"syncTarget":{"workspace":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"workspace":"root:org:ws","namespace":"test"}`,
				}),
			gvr:          schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: nil,
			toResources: []runtime.Object{
				namespace("test", "root:org:ws", map[string]string{"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync"}, nil),
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Sync",
				}, map[string]string{
					"deletion.internal.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5":   time.Now().Format(time.RFC3339),
					"experimental.status.workload.kcp.dev/2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": `{"replicas":15}`,
				}, []string{"workload.kcp.dev/syncer-2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5"}),
			},
			resourceToProcessName:     "theDeployment",
			syncTargetName:            "us-west1",
			advancedSchedulingEnabled: true,

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []kcptesting.Action{
				updateDeploymentAction("test",
					changeUnstructured(
						toUnstructured(t, changeDeployment(
							deployment("theDeployment", "test", "root:org:ws", map[string]string{}, map[string]string{}, nil))),
						// The following "changes" are required for the test to pass, as it expects some empty/nil fields to be there
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
			logger := klog.FromContext(ctx)

			kcpLogicalCluster := tc.upstreamLogicalCluster
			if tc.syncTargetUID == "" {
				tc.syncTargetUID = types.UID("syncTargetUID")
			}
			if tc.syncTargetClusterName.Empty() {
				tc.syncTargetClusterName = "root:org:ws"
			}

			var allFromResources []runtime.Object
			allFromResources = append(allFromResources, tc.fromNamespace)
			if tc.fromResource != nil {
				allFromResources = append(allFromResources, tc.fromResource)
			}
			fromClient := dynamicfake.NewSimpleDynamicClient(scheme, allFromResources...)
			toClusterClient := kcpfakedynamic.NewSimpleDynamicClient(scheme, tc.toResources...)

			syncTargetKey := workloadv1alpha1.ToSyncTargetKey(tc.syncTargetClusterName, tc.syncTargetName)
			fromInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(fromClient, time.Hour, metav1.NamespaceAll, func(o *metav1.ListOptions) {
				o.LabelSelector = workloadv1alpha1.InternalDownstreamClusterLabel + "=" + syncTargetKey
			})
			toInformers := kcpdynamicinformer.NewFilteredDynamicSharedInformerFactory(toClusterClient, time.Hour, func(o *metav1.ListOptions) {
				o.LabelSelector = workloadv1alpha1.ClusterResourceStateLabelPrefix + syncTargetKey + "=" + string(workloadv1alpha1.ResourceStateSync)
			})

			setupServersideApplyPatchReactor(toClusterClient)
			fromClientResourceWatcherStarted := setupWatchReactor(tc.gvr.Resource, fromClient)
			toClientResourceWatcherStarted := setupClusterWatchReactor(tc.gvr.Resource, toClusterClient)

			fakeInformers := newFakeSyncerInformers(tc.gvr, toInformers, fromInformers)
			controller, err := NewStatusSyncer(logger, kcpLogicalCluster, tc.syncTargetName, syncTargetKey, tc.advancedSchedulingEnabled, toClusterClient, fromClient, fromInformers, fakeInformers, tc.syncTargetUID)
			require.NoError(t, err)

			toInformers.ForResource(tc.gvr).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{})

			fromInformers.Start(ctx.Done())
			toInformers.Start(ctx.Done())

			fromInformers.WaitForCacheSync(ctx.Done())
			toInformers.WaitForCacheSync(ctx.Done())

			<-fromClientResourceWatcherStarted
			<-toClientResourceWatcherStarted

			fromClient.ClearActions()
			toClusterClient.ClearActions()

			key := tc.fromNamespace.Name + "/" + tc.resourceToProcessName
			err = controller.process(context.Background(),
				schema.GroupVersionResource{
					Group:    "apps",
					Version:  "v1",
					Resource: "deployments",
				},
				key,
			)
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Empty(t, cmp.Diff(tc.expectActionsOnFrom, fromClient.Actions()))
			assert.Empty(t, cmp.Diff(tc.expectActionsOnTo, toClusterClient.Actions(), cmp.AllowUnexported(logicalcluster.Path{})))
		})
	}
}

func setupServersideApplyPatchReactor(toClient *kcpfakedynamic.FakeDynamicClusterClientset) {
	toClient.PrependReactor("patch", "*", func(action kcptesting.Action) (handled bool, ret runtime.Object, err error) {
		patchAction := action.(kcptesting.PatchAction)
		if patchAction.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil
		}
		return true, nil, err
	})
}

func setupWatchReactor(resource string, client *dynamicfake.FakeDynamicClient) chan struct{} {
	watcherStarted := make(chan struct{})
	client.PrependWatchReactor(resource, func(action clienttesting.Action) (handled bool, ret watch.Interface, err error) {
		gvr := action.GetResource()
		ns := action.GetNamespace()
		watch, err := client.Tracker().Watch(gvr, ns)
		if err != nil {
			return false, nil, err
		}
		close(watcherStarted)
		return true, watch, nil
	})
	return watcherStarted
}

func setupClusterWatchReactor(resource string, client *kcpfakedynamic.FakeDynamicClusterClientset) chan struct{} {
	watcherStarted := make(chan struct{})
	client.PrependWatchReactor(resource, func(action kcptesting.Action) (bool, watch.Interface, error) {
		cluster := action.GetCluster()
		gvr := action.GetResource()
		ns := action.GetNamespace()
		var watcher watch.Interface
		var err error
		switch cluster {
		case logicalcluster.Wildcard:
			watcher, err = client.Tracker().Watch(gvr, ns)
		default:
			watcher, err = client.Tracker().Cluster(cluster).Watch(gvr, ns)
		}
		close(watcherStarted)
		return true, watcher, err
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

func setNestedField(value interface{}, fields ...string) unstructuredChange {
	return func(d *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(d.UnstructuredContent(), value, fields...)
	}
}

func deploymentAction(verb, namespace string, subresources ...string) kcptesting.ActionImpl {
	return kcptesting.ActionImpl{
		Namespace:   namespace,
		Cluster:     logicalcluster.New("root:org:ws"),
		Verb:        verb,
		Resource:    schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		Subresource: strings.Join(subresources, "/"),
	}
}

func updateDeploymentAction(namespace string, object runtime.Object, subresources ...string) kcptesting.UpdateActionImpl {
	return kcptesting.UpdateActionImpl{
		ActionImpl: deploymentAction("update", namespace, subresources...),
		Object:     object,
	}
}

type fakeSyncerInformers struct {
	upstreamInformer   kcpkubernetesinformers.GenericClusterInformer
	downStreamInformer informers.GenericInformer
}

func newFakeSyncerInformers(gvr schema.GroupVersionResource, upstreamInformers kcpdynamicinformer.DynamicSharedInformerFactory, downStreamInformers dynamicinformer.DynamicSharedInformerFactory) *fakeSyncerInformers {
	return &fakeSyncerInformers{
		upstreamInformer:   upstreamInformers.ForResource(gvr),
		downStreamInformer: downStreamInformers.ForResource(gvr),
	}
}

func (f *fakeSyncerInformers) AddUpstreamEventHandler(handler resourcesync.ResourceEventHandlerPerGVR) {
}
func (f *fakeSyncerInformers) AddDownstreamEventHandler(handler resourcesync.ResourceEventHandlerPerGVR) {
}
func (f *fakeSyncerInformers) InformerForResource(gvr schema.GroupVersionResource) (*resourcesync.SyncerInformer, bool) {
	return &resourcesync.SyncerInformer{
		UpstreamInformer:   f.upstreamInformer,
		DownstreamInformer: f.downStreamInformer,
	}, true
}
func (f *fakeSyncerInformers) SyncableGVRs() (map[schema.GroupVersionResource]*resourcesync.SyncerInformer, error) {
	return map[schema.GroupVersionResource]*resourcesync.SyncerInformer{{Group: "apps", Version: "v1", Resource: "deployments"}: nil}, nil
}
func (f *fakeSyncerInformers) Start(ctx context.Context, numThreads int) {}
