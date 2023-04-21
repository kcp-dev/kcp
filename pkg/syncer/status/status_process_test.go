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
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kcpfakedynamic "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/dynamic/fake"
	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/syncer/indexers"
	"github.com/kcp-dev/kcp/pkg/syncer/synctarget"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
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

var _ ddsif.GVRSource = (*mockedGVRSource)(nil)

type mockedGVRSource struct {
}

func (s *mockedGVRSource) GVRs() map[schema.GroupVersionResource]ddsif.GVRPartialMetadata {
	return map[schema.GroupVersionResource]ddsif.GVRPartialMetadata{
		appsv1.SchemeGroupVersion.WithResource("deployments"): {
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Singular: "deployment",
				Kind:     "Deployment",
			},
		},
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
			Resource: "configmaps",
		}: {
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Singular: "configmap",
				Kind:     "ConfigMap",
			},
		},
		{
			Version:  "v1",
			Resource: "secrets",
		}: {
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Singular: "secret",
				Kind:     "Secret",
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

func TestStatusSyncerProcess(t *testing.T) {
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
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				},
				map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget":{"cluster":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"cluster":"root:org:ws","namespace":"test"}`,
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				}, nil, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Sync",
				}, nil, nil),
			},
			resourceToProcessName: "theDeployment",
			syncTargetName:        "us-west1",

			expectActionsOnFrom: []clienttesting.Action{},
			expectActionsOnTo: []kcptesting.Action{
				updateDeploymentAction("test",
					toUnstructured(t, changeDeployment(
						deployment("theDeployment", "test", "root:org:ws", map[string]string{
							"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Sync",
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
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				},
				map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget":{"cluster":"root:org:ws","name":"us-west1","uid":"ANOTHERSYNCTARGETUID"},"cluster":"root:org:ws","namespace":"test"}`,
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				}, nil, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Sync",
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
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				},
				map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget":{"cluster":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"cluster":"root:org:ws","namespace":"test"}`,
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", nil, nil, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Sync",
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
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				},
				map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget":{"cluster":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"cluster":"root:org:ws","namespace":"test"}`,
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				}, nil, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Sync",
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
							"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Sync",
						}, map[string]string{
							"experimental.status.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "{\"replicas\":15}",
						}, nil)))),
			},
		},
		"StatusSyncer with AdvancedScheduling, deletion: object exists upstream": {
			upstreamLogicalCluster: "root:org:ws",
			fromNamespace: namespace("kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "",
				map[string]string{
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				},
				map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget":{"cluster":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"cluster":"root:org:ws","namespace":"test"}`,
				}),
			gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: changeDeployment(
				deployment("theDeployment", "kcp0124d7647eb6a00b1fcb6f2252201601634989dd79deb7375c373973", "", map[string]string{
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				}, nil, nil),
				addDeploymentStatus(appsv1.DeploymentStatus{
					Replicas: 15,
				})),
			toResources: []runtime.Object{
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Sync",
				}, map[string]string{
					"deletion.internal.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g":   time.Now().Format(time.RFC3339),
					"experimental.status.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "{\"replicas\":15}",
				}, []string{"workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g"}),
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
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				},
				map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget":{"cluster":"root:org:ws","name":"us-west1","uid":"syncTargetUID"},"cluster":"root:org:ws","namespace":"test"}`,
				}),
			gvr:          schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			fromResource: nil,
			toResources: []runtime.Object{
				namespace("test", "root:org:ws", map[string]string{"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Sync"}, nil),
				deployment("theDeployment", "test", "root:org:ws", map[string]string{
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Sync",
				}, map[string]string{
					"deletion.internal.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g":   time.Now().Format(time.RFC3339),
					"experimental.status.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": `{"replicas":15}`,
				}, []string{"workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g"}),
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

			ddsifForUpstreamSyncer, err := ddsif.NewDiscoveringDynamicSharedInformerFactory(toClusterClient, nil, nil, &mockedGVRSource{}, cache.Indexers{})
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

			setupServersideApplyPatchReactor(toClusterClient)
			fromClientResourceWatcherStarted := setupWatchReactor(t, tc.gvr.Resource, fromClient)
			toClientResourceWatcherStarted := setupClusterWatchReactor(t, tc.gvr.Resource, toClusterClient)

			getShardAccess := func(clusterName logicalcluster.Name) (synctarget.ShardAccess, bool, error) {
				return synctarget.ShardAccess{
					SyncerClient: toClusterClient,
					SyncerDDSIF:  ddsifForUpstreamSyncer,
				}, true, nil
			}
			controller, err := NewStatusSyncer(logger, kcpLogicalCluster, tc.syncTargetName, syncTargetKey, tc.advancedSchedulingEnabled, getShardAccess, fromClient, ddsifForDownstream, tc.syncTargetUID)
			require.NoError(t, err)

			ddsifForUpstreamSyncer.Start(ctx.Done())
			ddsifForDownstream.Start(ctx.Done())

			go ddsifForUpstreamSyncer.StartWorker(ctx)
			go ddsifForDownstream.StartWorker(ctx)

			<-fromClientResourceWatcherStarted
			<-toClientResourceWatcherStarted

			// The only GVRs we care about are the 4 listed below
			t.Logf("waiting for upstream and downstream dynamic informer factories to be synced")
			gvrs := sets.New[string](
				schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}.String(),
				schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}.String(),
				schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}.String(),
				schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}.String(),
			)
			require.Eventually(t, func() bool {
				syncedUpstream, _ := ddsifForUpstreamSyncer.Informers()
				foundUpstream := sets.New[string]()
				for gvr := range syncedUpstream {
					foundUpstream.Insert(gvr.String())
				}

				syncedDownstream, _ := ddsifForDownstream.Informers()
				foundDownstream := sets.New[string]()
				for gvr := range syncedDownstream {
					foundDownstream.Insert(gvr.String())
				}
				return foundUpstream.IsSuperset(gvrs) && foundDownstream.IsSuperset(gvrs)
			}, wait.ForeverTestTimeout, 100*time.Millisecond)
			t.Logf("upstream and downstream dynamic informer factories are synced")

			// Now that we know the informer factories have the GVRs we care about synced, we need to clear the
			// actions so our expectations will be accurate.
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
				require.Error(t, err)
			} else {
				require.NoError(t, err)
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
		ClusterPath: logicalcluster.NewPath("root:org:ws"),
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
