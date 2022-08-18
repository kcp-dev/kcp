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
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
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

var _ dynamic.ClusterInterface = (*mockedDynamicCluster)(nil)

type mockedDynamicCluster struct {
	client *dynamicfake.FakeDynamicClient
}

func (mdc *mockedDynamicCluster) Cluster(name logicalcluster.Name) dynamic.Interface {
	return mdc.client
}

func TestSyncerProcess(t *testing.T) {
	tests := map[string]struct {
		fromNamespace *corev1.Namespace
		gvr           schema.GroupVersionResource
		fromResource  runtime.Object
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
		"StatusSyncer upsert to existing resource": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"workspace":"root:org:ws","namespace":"test"}`,
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
			resourceToProcessLogicalClusterName: "",
			resourceToProcessName:               "theDeployment",
			syncTargetName:                      "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
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
		"StatusSyncer upstream deletion": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"workspace":"root:org:ws","namespace":"test"}`,
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
			resourceToProcessLogicalClusterName: "",
			resourceToProcessName:               "theDeployment",
			syncTargetName:                      "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo:   []clienttesting.Action{},
		},
		"StatusSyncer with AdvancedScheduling, update status upstream": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"workspace":"root:org:ws","namespace":"test"}`,
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
			resourceToProcessLogicalClusterName: "",
			resourceToProcessName:               "theDeployment",
			syncTargetName:                      "us-west1",
			advancedSchedulingEnabled:           true,

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
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
					"kcp.dev/namespace-locator": `{"workspace":"root:org:ws","namespace":"test"}`,
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
			resourceToProcessLogicalClusterName: "",
			resourceToProcessName:               "theDeployment",
			syncTargetName:                      "us-west1",
			advancedSchedulingEnabled:           true,

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo:   []clienttesting.Action{},
		},
		"StatusSyncer with AdvancedScheduling, deletion: object does not exists upstream": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				},
				map[string]string{
					"kcp.dev/namespace-locator": `{"workspace":"root:org:ws","namespace":"test"}`,
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
			resourceToProcessLogicalClusterName: "",
			resourceToProcessName:               "theDeployment",
			syncTargetName:                      "us-west1",
			advancedSchedulingEnabled:           true,

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []clienttesting.Action{
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

			kcpLogicalCluster := logicalcluster.New(tc.upstreamLogicalCluster)
			if tc.syncTargetUID == "" {
				tc.syncTargetUID = types.UID("syncTargetUID")
			}
			if tc.syncTargetWorkspace.Empty() {
				tc.syncTargetWorkspace = logicalcluster.New("root:org:ws")
			}

			var allFromResources []runtime.Object
			allFromResources = append(allFromResources, tc.fromNamespace)
			if tc.fromResource != nil {
				allFromResources = append(allFromResources, tc.fromResource)
			}
			fromClient := dynamicfake.NewSimpleDynamicClient(scheme, allFromResources...)
			toClient := dynamicfake.NewSimpleDynamicClient(scheme, tc.toResources...)
			toClusterClient := &mockedDynamicCluster{
				client: toClient,
			}

			syncTargetKey := workloadv1alpha1.ToSyncTargetKey(tc.syncTargetWorkspace, tc.syncTargetName)
			fromInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(fromClient, time.Hour, metav1.NamespaceAll, func(o *metav1.ListOptions) {
				o.LabelSelector = workloadv1alpha1.InternalDownstreamClusterLabel + "=" + syncTargetKey
			})
			toInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(toClusterClient.Cluster(logicalcluster.Wildcard), time.Hour, metav1.NamespaceAll, func(o *metav1.ListOptions) {
				o.LabelSelector = workloadv1alpha1.ClusterResourceStateLabelPrefix + syncTargetKey + "=" + string(workloadv1alpha1.ResourceStateSync)
			})

			setupServersideApplyPatchReactor(toClient)
			fromClientNamespaceWatcherStarted := setupWatchReactor("namespaces", fromClient)
			fromClientResourceWatcherStarted := setupWatchReactor(tc.gvr.Resource, fromClient)
			toClientResourceWatcherStarted := setupWatchReactor(tc.gvr.Resource, toClient)

			gvrs := []schema.GroupVersionResource{
				{Group: "", Version: "v1", Resource: "namespaces"},
				tc.gvr,
			}
			controller, err := NewStatusSyncer(gvrs, kcpLogicalCluster, tc.syncTargetName, syncTargetKey, tc.advancedSchedulingEnabled, toClusterClient, fromClient, toInformers, fromInformers, tc.syncTargetUID)
			require.NoError(t, err)

			toInformers.ForResource(tc.gvr).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{})

			fromInformers.Start(ctx.Done())
			toInformers.Start(ctx.Done())

			fromInformers.WaitForCacheSync(ctx.Done())
			toInformers.WaitForCacheSync(ctx.Done())

			<-fromClientResourceWatcherStarted
			<-fromClientNamespaceWatcherStarted
			<-toClientResourceWatcherStarted

			fromClient.ClearActions()
			toClient.ClearActions()

			key := tc.fromNamespace.Name + "/" + clusters.ToClusterAwareKey(logicalcluster.New(tc.resourceToProcessLogicalClusterName), tc.resourceToProcessName)
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

func deploymentAction(verb, namespace string, subresources ...string) clienttesting.ActionImpl {
	return clienttesting.ActionImpl{
		Namespace:   namespace,
		Verb:        verb,
		Resource:    schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		Subresource: strings.Join(subresources, "/"),
	}
}

func updateDeploymentAction(namespace string, object runtime.Object, subresources ...string) clienttesting.UpdateActionImpl {
	return clienttesting.UpdateActionImpl{
		ActionImpl: deploymentAction("update", namespace, subresources...),
		Object:     object,
	}
}
