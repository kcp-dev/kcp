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

package upsync

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpdynamicinformer "github.com/kcp-dev/client-go/dynamic/dynamicinformer"
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpfakedynamic "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/dynamic/fake"
	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/syncer/resourcesync"
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
	"k8s.io/client-go/dynamic/dynamicinformer"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/informers"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

var scheme *runtime.Scheme

func init() {
	scheme = runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
}

func toUnstructured(t require.TestingT, obj metav1.Object) *unstructured.Unstructured {
	var result unstructured.Unstructured
	err := scheme.Convert(obj, &result, nil)
	require.NoError(t, err)

	return &result
}
func TestUpsyncerprocess(t *testing.T) {
	tests := map[string]struct {
		fromNamespace *corev1.Namespace
		gvr           schema.GroupVersionResource
		fromResource  runtime.Object
		toResources   []runtime.Object

		resourceToProcessName string

		upstreamURL            string
		upstreamLogicalCluster string
		syncTargetName         string
		syncTargetWorkspace    logicalcluster.Name
		syncTargetUID          types.UID
		expectError            bool
		expectActionsOnFrom    []clienttesting.Action
		expectActionsOnTo      []kcptesting.Action
		isUpstream             bool
		updateType             []UpdateType
	}{
		"StatusSyncer upsyncs namespaced resources": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "", map[string]string{
				"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Upsync",
			},
				map[string]string{
					"kcp.dev/namespace-locator": `{"syncTarget": {"workspace":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "workspace":"root:org:ws","namespace":"test"}`,
				},
			),
			gvr: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"},
			fromResource: getPVC("test-pvc", "test", "", map[string]string{
				"internal.workload.kcp.dev/cluster":                              "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "root:org:ws": "Upsync",
			}, nil, nil, "1"),
			toResources:           []runtime.Object{},
			resourceToProcessName: "test-pvc",
			syncTargetName:        "us-west1",
			expectActionsOnFrom: []clienttesting.Action{clienttesting.NewGetAction(schema.GroupVersionResource{Group: "",
				Version:  "v1",
				Resource: "persistentvolumeclaims"}, "test", "test-pvc")},
			expectActionsOnTo: []kcptesting.Action{
				// kcptesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}, logicalcluster.New("root:org:ws"), "test", "test-pvc"),
				kcptesting.NewCreateAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}, logicalcluster.New("root:org:ws"), "test", toUnstructured(t, getPVC("test-pvc", "test", "", map[string]string{
					"internal.workload.kcp.dev/cluster":                              "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "root:org:ws": "Upsync",
				}, map[string]string{"kcp.dev/resource-version": "1"}, nil, "1"))),
				kcptesting.NewUpdateSubresourceAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}, logicalcluster.New("root:org:ws"), "spec", "test",
					toUnstructured(t, getPVC("test-pvc", "test", "", map[string]string{
						"internal.workload.kcp.dev/cluster":                              "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
						workloadv1alpha1.ClusterResourceStateLabelPrefix + "root:org:ws": "Upsync",
					}, map[string]string{"kcp.dev/resource-version": "1"}, nil, "1"))),
				kcptesting.NewUpdateSubresourceAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}, logicalcluster.New("root:org:ws"), "status", "test",
					toUnstructured(t, getPVC("test-pvc", "test", "", map[string]string{
						"internal.workload.kcp.dev/cluster":                              "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
						workloadv1alpha1.ClusterResourceStateLabelPrefix + "root:org:ws": "Upsync",
					}, map[string]string{"kcp.dev/resource-version": "1"}, nil, "1"))),
			},
			isUpstream: false,
			updateType: []UpdateType{MetadataUpdate, SpecUpdate},
		},
		"StatusSyncer upsyncs cluster-wide resources": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace:          nil,
			gvr:                    schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"},
			fromResource: getPV(getPVC("test", "test", "", map[string]string{
				"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Upsync",
			},
				map[string]string{
					"kcp.dev/namespace-locator": `{"syncTarget": {"workspace":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "workspace":"root:org:ws","namespace":""}`,
				}, nil, "1")),
			toResources:           []runtime.Object{},
			resourceToProcessName: "test-pv",
			syncTargetName:        "us-west1",
			expectActionsOnFrom:   []clienttesting.Action{clienttesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, "", "test-pv")},
			expectActionsOnTo: []kcptesting.Action{
				// kcptesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, logicalcluster.New("root:org:ws"), "", "test-pv"),
				kcptesting.NewCreateAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, logicalcluster.New("root:org:ws"), "", toUnstructured(t, getPV(getPVC("test", "test", "", map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Upsync",
				}, map[string]string{"kcp.dev/resource-version": "1"}, nil, "1")))),
				kcptesting.NewUpdateSubresourceAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, logicalcluster.New("root:org:ws"), "spec", "", toUnstructured(t, getPV(getPVC("test", "test", "", map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Upsync",
				}, map[string]string{"kcp.dev/resource-version": "1"}, nil, "1")))),
				kcptesting.NewUpdateSubresourceAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, logicalcluster.New("root:org:ws"), "status", "", toUnstructured(t, getPV(getPVC("test", "test", "", map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Upsync",
				}, map[string]string{"kcp.dev/resource-version": "1"}, nil, "1")))),
			},
			isUpstream: false,
			updateType: []UpdateType{MetadataUpdate},
		},

		"Status Syncer udpates namespaced resources": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "", map[string]string{
				"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Upsync",
			},
				map[string]string{
					"kcp.dev/namespace-locator": `{"syncTarget": {"workspace":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "workspace":"root:org:ws","namespace":"test"}`,
				},
			),
			gvr: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"},
			fromResource: getPVC("test-pvc", "test", "", map[string]string{
				"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Upsync",
			}, nil, nil, "2"),
			toResources: []runtime.Object{
				getPVC("test-pvc", "test", "root:org:ws", map[string]string{
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Upsync",
				}, map[string]string{"kcp.dev/resource-version": "1"}, nil, "1"),
			},
			resourceToProcessName: "test-pvc",
			syncTargetName:        "us-west1",
			expectActionsOnFrom:   []clienttesting.Action{clienttesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}, "test", "test-pvc")},
			expectActionsOnTo: []kcptesting.Action{
				// kcptesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}, logicalcluster.New("root:org:ws"), "test", "test-pvc"),
				kcptesting.NewUpdateSubresourceAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}, logicalcluster.New("root:org:ws"), "metadata", "test",
					toUnstructured(t, getPVC("test-pvc", "test", "", map[string]string{
						"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
						workloadv1alpha1.ClusterResourceStateLabelPrefix + "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Upsync",
					}, map[string]string{"kcp.dev/resource-version": "2"}, nil, "2"))),
			},
			isUpstream: false,
			updateType: []UpdateType{MetadataUpdate},
		},
		"StatusSyncer updates cluster-wide resources": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace:          nil,
			gvr:                    schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"},
			fromResource: getPV(getPVC("test", "test", "", map[string]string{
				"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Upsync",
			},
				map[string]string{
					"kcp.dev/namespace-locator": `{"syncTarget": {"workspace":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "workspace":"root:org:ws","namespace":""}`,
				}, nil, "2")),
			toResources: []runtime.Object{
				getPV(getPVC("test", "test", "root:org:ws", map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Upsync",
				}, map[string]string{
					"kcp.dev/resource-version": "1",
				}, nil, "1")),
			},
			resourceToProcessName: "test-pv",
			syncTargetName:        "us-west1",
			expectActionsOnFrom:   []clienttesting.Action{clienttesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, "", "test-pv")},
			expectActionsOnTo: []kcptesting.Action{
				// kcptesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, logicalcluster.New("root:org:ws"), "", "test-pv"),
				kcptesting.NewUpdateSubresourceAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, logicalcluster.New("root:org:ws"), "metadata", "", toUnstructured(t, getPV(getPVC("test", "test", "", map[string]string{
					"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Upsync",
				}, map[string]string{"kcp.dev/resource-version": "2"}, nil, "2")))),
			},
			isUpstream: false,
			updateType: []UpdateType{MetadataUpdate},
		},
		"StatusSyncer deletes upstream namespaced resources": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "", map[string]string{
				"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Upsync",
			},
				map[string]string{
					"kcp.dev/namespace-locator": `{"syncTarget": {"workspace":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "workspace":"root:org:ws","namespace":"test"}`,
				},
			),
			gvr: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"},
			fromResource: namespace("kcp-1dtzz0tyij19", "", map[string]string{
				"internal.workload.kcp.dev/cluster": "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5": "Upsync",
			},
				map[string]string{
					"kcp.dev/namespace-locator": `{"syncTarget": {"workspace":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "workspace":"root:org:ws","namespace":"test"}`,
				},
			),
			toResources: []runtime.Object{
				getPVC("test-pvc", "test", "root:org:ws", map[string]string{
					"internal.workload.kcp.dev/cluster":                              "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "root:org:ws": "Upsync",
				}, nil, nil, "1"),
			},
			resourceToProcessName: "test-pvc",
			syncTargetName:        "us-west1",
			expectActionsOnFrom:   []clienttesting.Action{},
			expectActionsOnTo: []kcptesting.Action{
				kcptesting.NewDeleteAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}, logicalcluster.New("root:org:ws"), "test", "test-pvc"),
			},
			isUpstream: true,
			updateType: []UpdateType{},
		},
		"StatusSyncer deletes upstream cluster-wide resources": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace:          nil,
			gvr:                    schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"},
			fromResource:           nil,
			toResources: []runtime.Object{
				getPV(getPVC("test", "test", "root:org:ws", map[string]string{
					"internal.workload.kcp.dev/cluster":                              "2gzO8uuQmIoZ2FE95zoOPKtrtGGXzzjAvtl6q5",
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "root:org:ws": "Upsync",
				}, nil, nil, "1")),
			},
			resourceToProcessName: "test-pv",
			syncTargetName:        "us-west1",
			expectActionsOnFrom: []clienttesting.Action{
				clienttesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, "", "test-pv"),
			},
			expectActionsOnTo: []kcptesting.Action{
				kcptesting.NewDeleteAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, logicalcluster.New("root:org:ws"), "", "test-pv"),
			},
			isUpstream: true,
			updateType: []UpdateType{},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			logger := klog.FromContext(ctx)

			kcpLogicalCluster := logicalcluster.New(tc.upstreamLogicalCluster)
			if tc.syncTargetUID == "" {
				tc.syncTargetUID = types.UID("syncTargetUID")
			}
			if tc.syncTargetWorkspace.Empty() {
				tc.syncTargetWorkspace = logicalcluster.New("root:org:ws")
			}

			var allFromResources []runtime.Object
			if tc.fromNamespace != nil {
				allFromResources = append(allFromResources, tc.fromNamespace)
			}

			if tc.fromResource != nil {
				allFromResources = append(allFromResources, tc.fromResource)
			}
			fromClient := dynamicfake.NewSimpleDynamicClient(scheme, allFromResources...)
			toClusterClient := kcpfakedynamic.NewSimpleDynamicClient(scheme, tc.toResources...)

			syncTargetKey := workloadv1alpha1.ToSyncTargetKey(tc.syncTargetWorkspace, tc.syncTargetName)
			fromInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(fromClient, time.Hour, metav1.NamespaceAll, func(o *metav1.ListOptions) {
				o.LabelSelector = workloadv1alpha1.InternalDownstreamClusterLabel + "=" + syncTargetKey
			})
			toInformers := kcpdynamicinformer.NewFilteredDynamicSharedInformerFactory(toClusterClient, time.Hour, func(o *metav1.ListOptions) {
				o.LabelSelector = workloadv1alpha1.ClusterResourceStateLabelPrefix + syncTargetKey + "=" + "Upsync"
			})

			setupServersideApplyPatchReactor(toClusterClient)
			fromClientResourceWatcherStarted := setupWatchReactor(tc.gvr.Resource, fromClient)
			toClientResourceWatcherStarted := setupClusterWatchReactor(tc.gvr.Resource, toClusterClient)

			fakeInformers := newFakeSyncerInformers(tc.gvr, toInformers, fromInformers)

			// upstream => to (kcp)
			// downstream => from (physical cluster)
			// to === kcp
			// from === physiccal
			controller, err := NewUpSyncer(logger, kcpLogicalCluster, tc.syncTargetName, syncTargetKey, toClusterClient, fromClient, toInformers, fromInformers, fakeInformers, tc.syncTargetUID)

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

			var key string
			if tc.fromNamespace != nil {
				key = tc.fromNamespace.Name + "/" + tc.resourceToProcessName
			} else {
				key = tc.resourceToProcessName
			}
			if tc.isUpstream {
				key += "#" + kcpLogicalCluster.String()
			}

			err = controller.process(context.Background(), tc.gvr, key, tc.isUpstream, tc.updateType)
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Empty(t, cmp.Diff(tc.expectActionsOnFrom, fromClient.Actions()))
			assert.Empty(t, cmp.Diff(tc.expectActionsOnTo, toClusterClient.Actions(), cmp.AllowUnexported(logicalcluster.Name{})))
		})
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
func (f *fakeSyncerInformers) Start(ctx context.Context, numThreads int) {}
func (f *fakeSyncerInformers) SyncableGVRs() (map[schema.GroupVersionResource]*resourcesync.SyncerInformer, error) {
	gvrs := map[schema.GroupVersionResource]*resourcesync.SyncerInformer{{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}: nil, {Group: "", Version: "v1", Resource: "persistentvolumes"}: nil}
	return gvrs, nil
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
	client.PrependWatchReactor(resource, func(action kcptesting.Action) (handled bool, ret watch.Interface, err error) {
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

func setupServersideApplyPatchReactor(toClient *kcpfakedynamic.FakeDynamicClusterClientset) {
	toClient.PrependReactor("patch", "*", func(action kcptesting.Action) (handled bool, ret runtime.Object, err error) {
		patchAction := action.(kcptesting.PatchAction)
		if patchAction.GetPatchType() != types.ApplyPatchType {
			return false, nil, nil
		}
		return true, nil, err
	})
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

func getPVC(name, namespace, clusterName string, labels, annotations map[string]string, finalizers []string, resourceVersion string) *corev1.PersistentVolumeClaim {
	if clusterName != "" {
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[logicalcluster.AnnotationKey] = clusterName
		return &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				ResourceVersion: resourceVersion,
				Name:            name,
				Namespace:       namespace,
				Labels:          labels,
				Annotations:     annotations,
				Finalizers:      finalizers,
			},
		}
	}
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			ResourceVersion: resourceVersion,
			Name:            name,
			Namespace:       namespace,
			Labels:          labels,
			Annotations:     annotations,
			Finalizers:      finalizers,
		},
	}

}

func getPV(pvc *corev1.PersistentVolumeClaim) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			ResourceVersion: pvc.GetResourceVersion(),
			Name:            pvc.GetName() + "-pv",
			Labels:          pvc.GetLabels(),
			Annotations:     pvc.GetAnnotations(),
			Finalizers:      pvc.GetFinalizers(),
		},
	}
}
