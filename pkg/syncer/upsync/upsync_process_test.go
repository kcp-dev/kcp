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

package upsync

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kcpfakedynamic "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/dynamic/fake"
	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/syncer/indexers"
)

var scheme *runtime.Scheme

func init() {
	scheme = runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
}

func toUnstructured(t require.TestingT, obj metav1.Object) *unstructured.Unstructured {
	var result unstructured.Unstructured
	err := scheme.Convert(obj, &result, nil)
	require.NoError(t, err)

	return &result
}

var _ ddsif.GVRSource = (*mockedGVRSource)(nil)

type mockedGVRSource struct {
	upsyncer bool
}

func (s *mockedGVRSource) GVRs() map[schema.GroupVersionResource]ddsif.GVRPartialMetadata {
	return map[schema.GroupVersionResource]ddsif.GVRPartialMetadata{
		{
			Version:  "v1",
			Resource: "namespaces",
		}: {
			Scope: apiextensionsv1.ClusterScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Singular: "namespace",
				Kind:     "Namespace",
			},
		},
		{
			Version:  "v1",
			Resource: "persistentvolumes",
		}: {
			Scope: apiextensionsv1.ClusterScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Singular: "persistentvolume",
				Kind:     "PersistentVolume",
			},
		},
		{
			Version:  "v1",
			Resource: "pods",
		}: {
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Singular: "pod",
				Kind:     "Pod",
			},
		},
	}
}

func (s *mockedGVRSource) Ready() bool {
	return true
}

func (s *mockedGVRSource) Subscribe() <-chan struct{} {
	return make(<-chan struct{})
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
		includeStatus          bool
	}{
		"Upsyncer upsyncs namespaced resources": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "", map[string]string{
				"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
			},
				map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":"test"}`,
				},
			),
			gvr: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
			fromResource: createPod("test-pod", "test", "", map[string]string{
				"internal.workload.kcp.io/cluster":                               "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "root:org:ws": "Upsync",
			}, nil, nil, "1"),
			toResources:           []runtime.Object{},
			resourceToProcessName: "test-pod",
			syncTargetName:        "us-west1",
			expectActionsOnFrom:   []clienttesting.Action{},
			expectActionsOnTo: []kcptesting.Action{
				// kcptesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}, logicalcluster.New("root:org:ws"), "test", "test-pvc"),
				kcptesting.NewCreateAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}, logicalcluster.NewPath("root:org:ws"), "test", toUnstructured(t, createPod("test-pod", "test", "", map[string]string{
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "root:org:ws": "Upsync",
				}, map[string]string{"kcp.io/resource-version": "1"}, []string{"workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g"}, ""))),
			},
			isUpstream:    false,
			includeStatus: false,
		},
		"Upsyncer upsyncs cluster-wide resources": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace:          nil,
			gvr:                    schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"},
			fromResource: createPV("test-pv", "", "", map[string]string{
				"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
			},
				map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":""}`,
				}, nil, "1"),
			toResources:           []runtime.Object{},
			resourceToProcessName: "test-pv",
			syncTargetName:        "us-west1",
			expectActionsOnFrom:   []clienttesting.Action{},
			expectActionsOnTo: []kcptesting.Action{
				// kcptesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, logicalcluster.New("root:org:ws"), "", "test-pv"),
				kcptesting.NewCreateAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, logicalcluster.NewPath("root:org:ws"), "", toUnstructured(t, createPV("test-pv", "", "", map[string]string{
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}, map[string]string{"kcp.io/resource-version": "1"}, []string{"workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g"}, ""))),
			},
			isUpstream:    false,
			includeStatus: false,
		},

		"Status Syncer udpates namespaced resources": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "", map[string]string{
				"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
			},
				map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":"test"}`,
				},
			),
			gvr: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
			fromResource: createPod("test-pod", "test", "", map[string]string{
				"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
			}, nil, nil, "2"),
			toResources: []runtime.Object{
				createPod("test-pod", "test", "root:org:ws", map[string]string{
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}, map[string]string{"kcp.io/resource-version": "1"}, nil, "1"),
			},
			resourceToProcessName: "test-pod",
			syncTargetName:        "us-west1",
			expectActionsOnFrom:   []clienttesting.Action{},
			expectActionsOnTo: []kcptesting.Action{
				// kcptesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}, logicalcluster.New("root:org:ws"), "test", "test-pvc"),
				kcptesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}, logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
				kcptesting.NewUpdateSubresourceAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}, logicalcluster.NewPath("root:org:ws"), "", "test",
					toUnstructured(t, createPod("test-pod", "test", "", map[string]string{
						workloadv1alpha1.ClusterResourceStateLabelPrefix + "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
					}, map[string]string{"kcp.io/resource-version": "2"}, []string{"workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g"}, "1"))),
			},
			isUpstream:    false,
			includeStatus: false,
		},
		"StatusSyncer updates cluster-wide resources": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace:          nil,
			gvr:                    schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"},
			fromResource: createPV("test-pv", "", "", map[string]string{
				"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
			},
				map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":""}`,
				}, nil, "2"),
			toResources: []runtime.Object{
				createPV("test-pv", "", "root:org:ws", map[string]string{
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}, map[string]string{
					"kcp.io/resource-version": "1",
				}, nil, "1"),
			},
			resourceToProcessName: "test-pv",
			syncTargetName:        "us-west1",
			expectActionsOnFrom:   []clienttesting.Action{},
			expectActionsOnTo: []kcptesting.Action{
				kcptesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, logicalcluster.NewPath("root:org:ws"), "", "test-pv"),
				kcptesting.NewUpdateSubresourceAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, logicalcluster.NewPath("root:org:ws"), "", "", toUnstructured(t, createPV("test-pv", "", "", map[string]string{
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}, map[string]string{"kcp.io/resource-version": "2"}, []string{"workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g"}, "1"))),
			},
			isUpstream:    false,
			includeStatus: false,
		},
		"StatusSyncer deletes upstream namespaced resources": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("test", "", map[string]string{
				"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
			},
				map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":"test"}`,
				},
			),
			gvr: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"},
			fromResource: namespace("kcp-1dtzz0tyij19", "", map[string]string{
				"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				workloadv1alpha1.ClusterResourceStateLabelPrefix + "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
			},
				map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":"test"}`,
				},
			),
			toResources: []runtime.Object{
				createPod("test-pod", "test", "root:org:ws", map[string]string{
					"internal.workload.kcp.io/cluster":                               "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "root:org:ws": "Upsync",
				}, nil, nil, "1"),
			},
			resourceToProcessName: "test-pod",
			syncTargetName:        "us-west1",
			expectActionsOnFrom:   []clienttesting.Action{},
			expectActionsOnTo: []kcptesting.Action{
				kcptesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}, logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
				kcptesting.NewDeleteAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}, logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
			},
			isUpstream:    true,
			includeStatus: false,
		},
		"StatusSyncer deletes upstream cluster-wide resources": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace:          nil,
			gvr:                    schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"},
			fromResource:           nil,
			toResources: []runtime.Object{
				createPV("test-pv", "", "root:org:ws", map[string]string{
					"internal.workload.kcp.io/cluster":                               "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					workloadv1alpha1.ClusterResourceStateLabelPrefix + "root:org:ws": "Upsync",
				}, nil, nil, "1"),
			},
			resourceToProcessName: "test-pv",
			syncTargetName:        "us-west1",
			expectActionsOnFrom:   []clienttesting.Action{},
			expectActionsOnTo: []kcptesting.Action{
				kcptesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, logicalcluster.NewPath("root:org:ws"), "", "test-pv"),
				kcptesting.NewDeleteAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, logicalcluster.NewPath("root:org:ws"), "", "test-pv"),
			},
			isUpstream:    true,
			includeStatus: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			logger := klog.FromContext(ctx)

			kcpLogicalCluster := logicalcluster.Name(tc.upstreamLogicalCluster)
			if tc.syncTargetUID == "" {
				tc.syncTargetUID = types.UID("syncTargetUID")
			}
			if tc.syncTargetWorkspace.Empty() {
				tc.syncTargetWorkspace = logicalcluster.Name("root:org:ws")
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

			ddsifForUpstreamUpsyncer, err := ddsif.NewDiscoveringDynamicSharedInformerFactory(toClusterClient, nil, nil, &mockedGVRSource{true}, cache.Indexers{})
			require.NoError(t, err)

			ddsifForDownstream, err := ddsif.NewScopedDiscoveringDynamicSharedInformerFactory(fromClient, nil,
				func(o *metav1.ListOptions) {
					o.LabelSelector = workloadv1alpha1.InternalDownstreamClusterLabel + "=" + syncTargetKey
				},
				&mockedGVRSource{},
				cache.Indexers{
					indexers.ByNamespaceLocatorIndexName: indexers.IndexByNamespaceLocator,
				},
			)
			require.NoError(t, err)

			var cacheSyncsForAlwaysRequiredGVRs []cache.InformerSynced
			if informer, err := ddsifForUpstreamUpsyncer.ForResource(corev1.SchemeGroupVersion.WithResource("namespaces")); err != nil {
				require.NoError(t, err)
			} else {
				cacheSyncsForAlwaysRequiredGVRs = append(cacheSyncsForAlwaysRequiredGVRs, informer.Informer().HasSynced)
			}
			if informer, err := ddsifForDownstream.ForResource(corev1.SchemeGroupVersion.WithResource("namespaces")); err != nil {
				require.NoError(t, err)
			} else {
				cacheSyncsForAlwaysRequiredGVRs = append(cacheSyncsForAlwaysRequiredGVRs, informer.Informer().HasSynced)
			}

			setupServersideApplyPatchReactor(toClusterClient)
			fromClientResourceWatcherStarted := setupWatchReactor(t, tc.gvr.Resource, fromClient)
			toClientResourceWatcherStarted := setupClusterWatchReactor(t, tc.gvr.Resource, toClusterClient)

			// upstream => to (kcp)
			// downstream => from (physical cluster)
			// to === kcp
			// from === physiccal
			controller, err := NewUpSyncer(logger, kcpLogicalCluster, tc.syncTargetName, syncTargetKey, toClusterClient, fromClient, ddsifForUpstreamUpsyncer, ddsifForDownstream, tc.syncTargetUID)
			require.NoError(t, err)

			gvrsUpdated := make(chan struct{})
			upstreamUpsyncerDDSIFUpdated := ddsifForUpstreamUpsyncer.Subscribe("upstreamUpsyncer")
			downstreamDDSIFUpdated := ddsifForDownstream.Subscribe("downstream")
			go func() {
				<-upstreamUpsyncerDDSIFUpdated
				t.Logf("%s: upstream ddsif synced", t.Name())
				<-downstreamDDSIFUpdated
				t.Logf("%s: downstream ddsif synced", t.Name())

				_, unsynced := ddsifForUpstreamUpsyncer.Informers()
				for _, gvr := range unsynced {
					informer, _ := ddsifForUpstreamUpsyncer.ForResource(gvr)
					cache.WaitForCacheSync(ctx.Done(), informer.Informer().HasSynced)
					t.Logf("%s: upstream ddsif informer synced for gvr %s", t.Name(), gvr.String())
				}
				_, unsynced = ddsifForDownstream.Informers()
				for _, gvr := range unsynced {
					informer, _ := ddsifForDownstream.ForResource(gvr)
					cache.WaitForCacheSync(ctx.Done(), informer.Informer().HasSynced)
					t.Logf("%s: downstream ddsif informer synced for gvr %s", t.Name(), gvr.String())
				}
				t.Logf("%s: gvrs updated", t.Name())
				gvrsUpdated <- struct{}{}
			}()

			ddsifForUpstreamUpsyncer.Start(ctx.Done())
			ddsifForDownstream.Start(ctx.Done())

			go ddsifForUpstreamUpsyncer.StartWorker(ctx)
			go ddsifForDownstream.StartWorker(ctx)

			<-fromClientResourceWatcherStarted
			<-toClientResourceWatcherStarted

			cache.WaitForCacheSync(ctx.Done(), cacheSyncsForAlwaysRequiredGVRs...)

			<-gvrsUpdated

			fromClient.ClearActions()
			toClusterClient.ClearActions()

			obj := &metav1.ObjectMeta{
				Name: tc.resourceToProcessName,
			}

			if tc.fromNamespace != nil {
				obj.Namespace = tc.fromNamespace.Name
			}

			var key string
			if tc.isUpstream {
				obj.Annotations = map[string]string{
					logicalcluster.AnnotationKey: kcpLogicalCluster.String(),
				}
				key, err = getKey(obj, Upstream)
			} else {
				key, err = getKey(obj, Downstream)
			}
			require.NoError(t, err)

			err = controller.process(context.Background(), tc.gvr, key, tc.isUpstream, tc.includeStatus)
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Empty(t, cmp.Diff(tc.expectActionsOnFrom, fromClient.Actions()))
			assert.Empty(t, cmp.Diff(tc.expectActionsOnTo, toClusterClient.Actions()))
		})
	}
}

func setupWatchReactor(t *testing.T, resource string, client *dynamicfake.FakeDynamicClient) chan struct{} {
	t.Helper()
	watcherStarted := make(chan struct{})
	client.PrependWatchReactor(resource, func(action clienttesting.Action) (handled bool, ret watch.Interface, err error) {
		gvr := action.GetResource()
		ns := action.GetNamespace()
		watch, err := client.Tracker().Watch(gvr, ns)
		if err != nil {
			return false, nil, err
		}
		t.Logf("%s: watcher started", t.Name())
		close(watcherStarted)
		return true, watch, nil
	})
	return watcherStarted
}

func setupClusterWatchReactor(t *testing.T, resource string, client *kcpfakedynamic.FakeDynamicClusterClientset) chan struct{} {
	t.Helper()
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
		t.Logf("%s: cluster watcher started", t.Name())
		close(watcherStarted)
		return true, watcher, err
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

func createPV(name, namespace, clusterName string, labels, annotations map[string]string, finalizers []string, resourceVersion string) *corev1.PersistentVolume {
	if clusterName != "" {
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[logicalcluster.AnnotationKey] = clusterName
		return &corev1.PersistentVolume{
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
	return &corev1.PersistentVolume{
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

func createPod(name, namespace, clusterName string, labels, annotations map[string]string, finalizers []string, resourceVersion string) *corev1.Pod {
	if clusterName != "" {
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[logicalcluster.AnnotationKey] = clusterName
		return &corev1.Pod{
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
	return &corev1.Pod{
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
