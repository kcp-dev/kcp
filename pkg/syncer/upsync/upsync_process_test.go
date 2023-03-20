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
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
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
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/util/sets"

	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/syncer/indexers"
	"github.com/kcp-dev/kcp/pkg/syncer/synctarget"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
)

var scheme *runtime.Scheme

func init() {
	scheme = runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
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
	type testCase struct {
		downstreamNamespace   *corev1.Namespace
		upstreamNamespaceName string
		gvr                   schema.GroupVersionResource
		downstreamResource    runtime.Object
		upstreamResource      runtime.Object
		doOnDownstream        func(tc testCase, client dynamic.Interface)

		resourceToProcessName string

		upstreamLogicalCluster    logicalcluster.Name
		syncTargetName            string
		syncTargetClusterName     logicalcluster.Name
		syncTargetUID             types.UID
		expectError               bool
		expectRequeue             bool
		expectActionsOnDownstream []clienttesting.Action
		expectActionsOnUpstream   []kcptesting.Action
		includeStatus             bool
	}
	tests := map[string]testCase{
		"Upsyncer upsyncs namespaced resources": {
			upstreamLogicalCluster: "root:org:ws",
			upstreamNamespaceName:  "test",
			downstreamNamespace: namespace("kcp-33jbiactwhg0").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				}).
				WithAnnotations(map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":"test"}`,
				}).
				Object(),
			gvr: corev1.SchemeGroupVersion.WithResource("pods"),
			downstreamResource: pod("test-pod").
				WithNamespace("kcp-33jbiactwhg0").
				WithResourceVersion("1").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				Unstructured(t).WithoutFields("status").Unstructured,
			upstreamResource:          nil,
			resourceToProcessName:     "test-pod",
			syncTargetName:            "us-west1",
			expectActionsOnDownstream: []clienttesting.Action{},
			expectActionsOnUpstream: []kcptesting.Action{
				kcptesting.NewCreateAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", pod("test-pod").
					WithNamespace("test").
					WithLabels(map[string]string{
						"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
					}).
					WithAnnotations(map[string]string{
						"workload.kcp.io/rv": "1",
					}).
					WithFinalizers("workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g").
					Unstructured(t).WithoutFields("status").Unstructured),
			},
			includeStatus: false,
		},
		"Upsyncer upsyncs namespaced resources with status": {
			upstreamLogicalCluster: "root:org:ws",
			upstreamNamespaceName:  "test",
			downstreamNamespace: namespace("kcp-33jbiactwhg0").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				}).
				WithAnnotations(map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":"test"}`,
				}).
				Object(),
			gvr: corev1.SchemeGroupVersion.WithResource("pods"),
			downstreamResource: pod("test-pod").
				WithNamespace("kcp-33jbiactwhg0").
				WithResourceVersion("1").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				Unstructured(t).WithField("status", map[string]interface{}{"phase": "Running"}).Unstructured,
			resourceToProcessName:     "test-pod",
			syncTargetName:            "us-west1",
			expectActionsOnDownstream: []clienttesting.Action{},
			expectActionsOnUpstream: []kcptesting.Action{
				kcptesting.NewCreateAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", pod("test-pod").
					WithNamespace("test").
					WithLabels(map[string]string{
						"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
					}).
					WithFinalizers("workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g").
					Unstructured(t).WithField("status", map[string]interface{}{"phase": "Running"}).Unstructured),
				kcptesting.NewUpdateSubresourceAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "status", "test", pod("test-pod").
					WithNamespace("test").
					WithLabels(map[string]string{
						"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
					}).
					WithFinalizers("workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g").
					Unstructured(t).WithField("status", map[string]interface{}{"phase": "Running"}).Unstructured),
				kcptesting.NewUpdateAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", pod("test-pod").
					WithNamespace("test").
					WithLabels(map[string]string{
						"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
					}).
					WithAnnotations(map[string]string{
						"workload.kcp.io/rv": "1",
					}).
					WithFinalizers("workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g").
					Unstructured(t).WithField("status", map[string]interface{}{"phase": "Running"}).Unstructured),
			},
			includeStatus: true,
		},
		"Upsyncer upsyncs cluster-wide resources": {
			upstreamLogicalCluster: "root:org:ws",
			upstreamNamespaceName:  "",
			downstreamNamespace:    nil,
			gvr:                    corev1.SchemeGroupVersion.WithResource("persistentvolumes"),
			downstreamResource: pv("test-pv").
				WithResourceVersion("1").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				WithAnnotations(map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":""}`,
				}).
				Unstructured(t).WithoutFields("status").Unstructured,
			upstreamResource:          nil,
			resourceToProcessName:     "test-pv",
			syncTargetName:            "us-west1",
			expectActionsOnDownstream: []clienttesting.Action{},
			expectActionsOnUpstream: []kcptesting.Action{
				kcptesting.NewCreateAction(corev1.SchemeGroupVersion.WithResource("persistentvolumes"), logicalcluster.NewPath("root:org:ws"), "", pv("test-pv").
					WithLabels(map[string]string{
						"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
					}).
					WithAnnotations(map[string]string{
						"workload.kcp.io/rv": "1",
					}).
					WithFinalizers("workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g").
					Unstructured(t).WithoutFields("status").Unstructured),
			},
			includeStatus: false,
		},
		"Upsyncer updates namespaced resources": {
			upstreamLogicalCluster: "root:org:ws",
			upstreamNamespaceName:  "test",
			downstreamNamespace: namespace("kcp-33jbiactwhg0").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				}).
				WithAnnotations(map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":"test"}`,
				}).
				Object(),
			gvr: corev1.SchemeGroupVersion.WithResource("pods"),
			downstreamResource: pod("test-pod").
				WithNamespace("kcp-33jbiactwhg0").
				WithResourceVersion("2").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				Unstructured(t).WithoutFields("status").Unstructured,
			upstreamResource: pod("test-pod").
				WithClusterName("root:org:ws").
				WithNamespace("test").
				WithResourceVersion("1").
				WithLabels(map[string]string{
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				WithAnnotations(map[string]string{
					"workload.kcp.io/rv": "1",
				}).
				Object(),
			resourceToProcessName:     "test-pod",
			syncTargetName:            "us-west1",
			expectActionsOnDownstream: []clienttesting.Action{},
			expectActionsOnUpstream: []kcptesting.Action{
				kcptesting.NewGetAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}, logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
				kcptesting.NewUpdateAction(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}, logicalcluster.NewPath("root:org:ws"), "test", pod("test-pod").
					WithNamespace("test").
					WithLabels(map[string]string{
						"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
					}).
					WithAnnotations(map[string]string{
						"workload.kcp.io/rv": "2",
					}).
					WithFinalizers("workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g").
					WithResourceVersion("1").
					Unstructured(t).WithoutFields("status").Unstructured),
			},
			includeStatus: false,
		},
		"Upsyncer updates namespaced resources, even if they already have a deletion timestamp, but requeue": {
			upstreamLogicalCluster: "root:org:ws",
			upstreamNamespaceName:  "test",
			downstreamNamespace: namespace("kcp-33jbiactwhg0").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				}).
				WithAnnotations(map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":"test"}`,
				}).
				Object(),
			gvr: corev1.SchemeGroupVersion.WithResource("pods"),
			downstreamResource: pod("test-pod").
				WithNamespace("kcp-33jbiactwhg0").
				WithResourceVersion("2").
				WithDeletionTimestamp().
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				Unstructured(t).WithoutFields("status").Unstructured,
			upstreamResource: pod("test-pod").
				WithClusterName("root:org:ws").
				WithNamespace("test").
				WithResourceVersion("1").
				WithLabels(map[string]string{
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				WithAnnotations(map[string]string{
					"workload.kcp.io/rv": "1",
				}).
				Object(),
			resourceToProcessName:     "test-pod",
			syncTargetName:            "us-west1",
			expectRequeue:             true,
			expectActionsOnDownstream: []clienttesting.Action{},
			expectActionsOnUpstream: []kcptesting.Action{
				kcptesting.NewGetAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
				kcptesting.NewUpdateAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", pod("test-pod").
					WithNamespace("test").
					WithResourceVersion("1").
					WithLabels(map[string]string{
						"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
					}).
					WithAnnotations(map[string]string{
						"workload.kcp.io/rv": "2",
					}).
					WithFinalizers("workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g").
					Unstructured(t).WithoutFields("status").Unstructured),
			},
			includeStatus: false,
		},
		"Upsyncer udpates namespaced resources with status": {
			upstreamLogicalCluster: "root:org:ws",
			upstreamNamespaceName:  "test",
			downstreamNamespace: namespace("kcp-33jbiactwhg0").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				}).
				WithAnnotations(map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":"test"}`,
				}).
				Object(),
			gvr: corev1.SchemeGroupVersion.WithResource("pods"),
			downstreamResource: pod("test-pod").
				WithNamespace("kcp-33jbiactwhg0").
				WithResourceVersion("11").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				Unstructured(t).WithField("status", map[string]interface{}{"phase": "Running"}).Unstructured,
			upstreamResource: pod("test-pod").
				WithClusterName("root:org:ws").
				WithNamespace("test").
				WithResourceVersion("1").
				WithLabels(map[string]string{
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				WithAnnotations(map[string]string{
					"workload.kcp.io/rv": "10",
				}).
				Unstructured(t).Unstructured,
			resourceToProcessName:     "test-pod",
			syncTargetName:            "us-west1",
			expectActionsOnDownstream: []clienttesting.Action{},
			expectActionsOnUpstream: []kcptesting.Action{
				kcptesting.NewGetAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
				kcptesting.NewUpdateAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", pod("test-pod").
					WithNamespace("test").
					WithResourceVersion("1").
					WithLabels(map[string]string{
						"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
					}).
					WithAnnotations(map[string]string{"workload.kcp.io/rv": "10"}).
					WithFinalizers("workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g").
					Unstructured(t).WithField("status", map[string]interface{}{"phase": "Running"}).Unstructured),
				kcptesting.NewUpdateSubresourceAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "status", "test", pod("test-pod").
					WithNamespace("test").
					WithResourceVersion("1").
					WithLabels(map[string]string{
						"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
					}).
					WithAnnotations(map[string]string{
						"workload.kcp.io/rv": "10",
					}).
					WithFinalizers("workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g").
					Unstructured(t).WithField("status", map[string]interface{}{"phase": "Running"}).Unstructured),
				kcptesting.NewUpdateAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", pod("test-pod").
					WithNamespace("test").
					WithResourceVersion("1").
					WithLabels(map[string]string{
						"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
					}).
					WithAnnotations(map[string]string{
						"workload.kcp.io/rv": "11",
					}).
					WithFinalizers("workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g").
					Unstructured(t).WithField("status", map[string]interface{}{"phase": "Running"}).Unstructured),
			},
			includeStatus: true,
		},
		"Upsyncer updates cluster-wide resources": {
			upstreamLogicalCluster: "root:org:ws",
			upstreamNamespaceName:  "",
			downstreamNamespace:    nil,
			gvr:                    corev1.SchemeGroupVersion.WithResource("persistentvolumes"),
			downstreamResource: pv("test-pv").
				WithResourceVersion("2").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				WithAnnotations(map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":""}`,
				}).
				Unstructured(t).WithoutFields("status").Unstructured,
			upstreamResource: pv("test-pv").
				WithClusterName("root:org:ws").
				WithResourceVersion("1").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				WithAnnotations(map[string]string{
					"workload.kcp.io/rv": "1",
				}).
				Object(),
			resourceToProcessName:     "test-pv",
			syncTargetName:            "us-west1",
			expectActionsOnDownstream: []clienttesting.Action{},
			expectActionsOnUpstream: []kcptesting.Action{
				kcptesting.NewGetAction(corev1.SchemeGroupVersion.WithResource("persistentvolumes"), logicalcluster.NewPath("root:org:ws"), "", "test-pv"),
				kcptesting.NewUpdateAction(corev1.SchemeGroupVersion.WithResource("persistentvolumes"), logicalcluster.NewPath("root:org:ws"), "", pv("test-pv").
					WithResourceVersion("1").
					WithLabels(map[string]string{
						"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
					}).
					WithAnnotations(map[string]string{
						"workload.kcp.io/rv": "2",
					}).
					WithFinalizers("workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g").
					Unstructured(t).WithoutFields("status").Unstructured),
			},
			includeStatus: false,
		},
		"Upsyncer deletes orphan upstream namespaced resources": {
			upstreamLogicalCluster: "root:org:ws",
			upstreamNamespaceName:  "test",
			downstreamNamespace: namespace("kcp-33jbiactwhg0").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				WithAnnotations(map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":"test"}`,
				}).
				Object(),
			gvr: corev1.SchemeGroupVersion.WithResource("pods"),
			upstreamResource: pod("test-pod").
				WithClusterName("root:org:ws").
				WithNamespace("test").
				WithResourceVersion("1").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				Object(),
			resourceToProcessName:     "test-pod",
			syncTargetName:            "us-west1",
			expectActionsOnDownstream: []clienttesting.Action{},
			expectActionsOnUpstream: []kcptesting.Action{
				kcptesting.NewGetAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
				kcptesting.NewDeleteAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
			},
			includeStatus: false,
		},
		"Upsyncer should delete orphan upstream namespaced resource, but it doesn't exist": {
			upstreamLogicalCluster: "root:org:ws",
			upstreamNamespaceName:  "test",
			downstreamNamespace: namespace("kcp-33jbiactwhg0").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				WithAnnotations(map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":"test"}`,
				}).
				Object(),
			gvr:                       corev1.SchemeGroupVersion.WithResource("pods"),
			resourceToProcessName:     "test-pod",
			syncTargetName:            "us-west1",
			expectActionsOnDownstream: []clienttesting.Action{},
			expectActionsOnUpstream: []kcptesting.Action{
				kcptesting.NewGetAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
			},
			includeStatus: false,
		},
		"Upsyncer deletes orphan upstream namespaced resources, even if the downstream namespace has also been deleted": {
			upstreamLogicalCluster: "root:org:ws",
			upstreamNamespaceName:  "test",
			downstreamNamespace:    nil,
			gvr:                    corev1.SchemeGroupVersion.WithResource("pods"),
			upstreamResource: pod("test-pod").
				WithClusterName("root:org:ws").
				WithNamespace("test").
				WithResourceVersion("1").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				Object(),
			resourceToProcessName:     "test-pod",
			syncTargetName:            "us-west1",
			expectActionsOnDownstream: []clienttesting.Action{},
			expectActionsOnUpstream: []kcptesting.Action{
				kcptesting.NewGetAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
				kcptesting.NewDeleteAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
			},
			includeStatus: false,
		},
		"Upsyncer deletes orphan upstream namespaced resources when downstream namespace is deleted": {
			upstreamLogicalCluster: "root:org:ws",
			upstreamNamespaceName:  "test",
			downstreamNamespace: namespace("kcp-33jbiactwhg0").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				WithAnnotations(map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":"test"}`,
				}).
				Object(),
			downstreamResource: pod("test-pod").
				WithClusterName("root:org:ws").
				WithNamespace("kcp-33jbiactwhg0").
				WithResourceVersion("1").
				WithLabels(map[string]string{
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				Object(),
			doOnDownstream: func(tc testCase, client dynamic.Interface) {
				err := client.Resource(namespaceGVR).Delete(context.Background(), "kcp-33jbiactwhg0", metav1.DeleteOptions{})
				require.NoError(t, err)
			},
			gvr: corev1.SchemeGroupVersion.WithResource("pods"),
			upstreamResource: pod("test-pod").
				WithClusterName("root:org:ws").
				WithNamespace("test").
				WithResourceVersion("1").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				Object(),
			resourceToProcessName:     "test-pod",
			syncTargetName:            "us-west1",
			expectActionsOnDownstream: []clienttesting.Action{},
			expectActionsOnUpstream: []kcptesting.Action{
				kcptesting.NewGetAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
				kcptesting.NewDeleteAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
			},
			includeStatus: false,
		},
		"Upsyncer deletes orphan upstream cluster-wide resources": {
			upstreamLogicalCluster: "root:org:ws",
			upstreamNamespaceName:  "",
			downstreamNamespace:    nil,
			gvr:                    corev1.SchemeGroupVersion.WithResource("persistentvolumes"),
			downstreamResource:     nil,
			upstreamResource: pv("test-pv").
				WithClusterName("root:org:ws").
				WithResourceVersion("1").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				Object(),
			resourceToProcessName:     "test-pv",
			syncTargetName:            "us-west1",
			expectActionsOnDownstream: []clienttesting.Action{},
			expectActionsOnUpstream: []kcptesting.Action{
				kcptesting.NewGetAction(corev1.SchemeGroupVersion.WithResource("persistentvolumes"), logicalcluster.NewPath("root:org:ws"), "", "test-pv"),
				kcptesting.NewDeleteAction(corev1.SchemeGroupVersion.WithResource("persistentvolumes"), logicalcluster.NewPath("root:org:ws"), "", "test-pv"),
			},
			includeStatus: false,
		},
		"Upsyncer deletes upstream resources if downstream resources have deletiontimestamp set": {
			upstreamLogicalCluster: "root:org:ws",
			upstreamNamespaceName:  "test",
			downstreamNamespace: namespace("kcp-33jbiactwhg0").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster": "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
				}).
				WithAnnotations(map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":"test"}`,
				}).Object(),
			gvr: corev1.SchemeGroupVersion.WithResource("pods"),
			downstreamResource: pod("test-pod").
				WithNamespace("kcp-33jbiactwhg0").
				WithDeletionTimestamp().
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				WithResourceVersion("1").
				Object(),
			upstreamResource: pod("test-pod").
				WithClusterName("root:org:ws").
				WithNamespace("test").
				WithResourceVersion("1").
				WithLabels(map[string]string{
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				WithAnnotations(map[string]string{
					"workload.kcp.io/rv": "1",
				}).
				Object(),
			resourceToProcessName:     "test-pod",
			syncTargetName:            "us-west1",
			expectActionsOnDownstream: []clienttesting.Action{},
			expectActionsOnUpstream: []kcptesting.Action{
				kcptesting.NewDeleteAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
			},
			includeStatus: false,
		},
		"Upsyncer handles deletion of upstream resources when they have finalizers set": {
			upstreamLogicalCluster: "root:org:ws",
			upstreamNamespaceName:  "test",
			downstreamNamespace: namespace("kcp-33jbiactwhg0").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				WithAnnotations(map[string]string{
					"kcp.io/namespace-locator": `{"syncTarget": {"cluster":"root:org:ws", "name":"us-west1", "uid":"syncTargetUID"}, "cluster":"root:org:ws","namespace":"test"}`,
				}).
				Object(),
			gvr: corev1.SchemeGroupVersion.WithResource("pods"),
			upstreamResource: pod("test-pod").
				WithClusterName("root:org:ws").
				WithNamespace("test").
				WithResourceVersion("1").
				WithLabels(map[string]string{
					"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
					"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
				}).
				WithFinalizers("workload.kcp.io/syncer-6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g").
				Object(),
			resourceToProcessName:     "test-pod",
			syncTargetName:            "us-west1",
			expectActionsOnDownstream: []clienttesting.Action{},
			expectActionsOnUpstream: []kcptesting.Action{
				kcptesting.NewGetAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
				kcptesting.NewUpdateAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", pod("test-pod").
					WithClusterName("root:org:ws").
					WithNamespace("test").
					WithResourceVersion("1").
					WithLabels(map[string]string{
						"internal.workload.kcp.io/cluster":                             "6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g",
						"state.workload.kcp.io/6ohB8yeXhwqTQVuBzJRgqcRJTpRjX7yTZu5g5g": "Upsync",
					}).
					Unstructured(t).Unstructured),
				kcptesting.NewDeleteAction(corev1.SchemeGroupVersion.WithResource("pods"), logicalcluster.NewPath("root:org:ws"), "test", "test-pod"),
			},
			includeStatus: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			logger := klog.FromContext(ctx)

			kcpLogicalCluster := tc.upstreamLogicalCluster
			syncTargetUID := tc.syncTargetUID
			if tc.syncTargetUID == "" {
				syncTargetUID = types.UID("syncTargetUID")
			}

			if tc.syncTargetClusterName.Empty() {
				tc.syncTargetClusterName = "root:org:ws"
			}

			var allFromResources []runtime.Object
			if tc.downstreamNamespace != nil {
				allFromResources = append(allFromResources, tc.downstreamNamespace)
			}
			if tc.downstreamResource != nil {
				allFromResources = append(allFromResources, tc.downstreamResource)
			}

			fromClient := dynamicfake.NewSimpleDynamicClient(scheme, allFromResources...)

			syncTargetKey := workloadv1alpha1.ToSyncTargetKey(tc.syncTargetClusterName, tc.syncTargetName)

			var toResources []runtime.Object
			if tc.upstreamResource != nil {
				toResources = append(toResources, tc.upstreamResource)
			}
			toClusterClient := kcpfakedynamic.NewSimpleDynamicClient(scheme, toResources...)

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

			setupServersideApplyPatchReactor(toClusterClient)
			fromClientResourceWatcherStarted := setupWatchReactor(t, tc.gvr.Resource, fromClient)
			toClientResourceWatcherStarted := setupClusterWatchReactor(t, tc.gvr.Resource, toClusterClient)

			// upstream => to (kcp)
			// downstream => from (physical cluster)
			// to === kcp
			// from === physical
			controller, err := NewUpSyncer(logger, kcpLogicalCluster, tc.syncTargetName, syncTargetKey, func(clusterName logicalcluster.Name) (synctarget.ShardAccess, error) {
				return synctarget.ShardAccess{
					UpsyncerClient: toClusterClient,
					UpsyncerDDSIF:  ddsifForUpstreamUpsyncer,
				}, nil
			}, fromClient, ddsifForDownstream, syncTargetUID)
			require.NoError(t, err)

			ddsifForUpstreamUpsyncer.Start(ctx.Done())
			ddsifForDownstream.Start(ctx.Done())

			go ddsifForUpstreamUpsyncer.StartWorker(ctx)
			go ddsifForDownstream.StartWorker(ctx)

			<-fromClientResourceWatcherStarted
			<-toClientResourceWatcherStarted

			// The only GVRs we care about are the 3 listed below
			t.Logf("waiting for upstream and downstream dynamic informer factories to be synced")
			gvrs := sets.NewString(
				corev1.SchemeGroupVersion.WithResource("namespaces").String(),
				corev1.SchemeGroupVersion.WithResource("pods").String(),
				corev1.SchemeGroupVersion.WithResource("persistentvolumes").String(),
			)
			require.Eventually(t, func() bool {
				syncedUpstream, _ := ddsifForUpstreamUpsyncer.Informers()
				foundUpstream := sets.NewString()
				for gvr := range syncedUpstream {
					foundUpstream.Insert(gvr.String())
				}

				syncedDownstream, _ := ddsifForDownstream.Informers()
				foundDownstream := sets.NewString()
				for gvr := range syncedDownstream {
					foundDownstream.Insert(gvr.String())
				}
				return foundUpstream.IsSuperset(gvrs) && foundDownstream.IsSuperset(gvrs)
			}, wait.ForeverTestTimeout, 100*time.Millisecond)
			t.Logf("upstream and downstream dynamic informer factories are synced")

			if tc.doOnDownstream != nil {
				tc.doOnDownstream(tc, fromClient)
			}

			fromClient.ClearActions()
			toClusterClient.ClearActions()

			obj := &metav1.ObjectMeta{
				Name:      tc.resourceToProcessName,
				Namespace: tc.upstreamNamespaceName,
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: kcpLogicalCluster.String(),
				},
			}

			key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
			require.NoError(t, err)

			if tc.includeStatus {
				controller.dirtyStatusKeys.Store(queueKey{
					key: key,
					gvr: tc.gvr,
				}, true)
			}

			requeue, err := controller.process(context.Background(), key, tc.gvr)
			assert.Equal(t, tc.expectRequeue, requeue)
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Empty(t, cmp.Diff(tc.expectActionsOnDownstream, fromClient.Actions()))
			assert.Empty(t, cmp.Diff(tc.expectActionsOnUpstream, toClusterClient.Actions()))
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

type resourceBuilder[Type metav1.Object] struct {
	obj Type
}

func (r *resourceBuilder[Type]) Object() Type {
	return r.obj
}

type unstructuredType struct {
	*unstructured.Unstructured
	t *testing.T
}

func (u *unstructuredType) WithoutFields(fieldsToPrune ...string) *unstructuredType {
	for _, field := range fieldsToPrune {
		unstructured.RemoveNestedField(u.Object, strings.Split(field, ".")...)
	}

	return u
}

func (u *unstructuredType) WithField(key string, value interface{}) *unstructuredType {
	err := unstructured.SetNestedField(u.Object, value, strings.Split(key, ".")...)
	require.NoError(u.t, err)
	return u
}

func (r *resourceBuilder[Type]) Unstructured(t *testing.T) *unstructuredType {
	var unstr unstructured.Unstructured
	err := scheme.Convert(r.obj, &unstr, nil)
	require.NoError(t, err)
	result := unstructuredType{&unstr, t}
	return &result
}

func (r *resourceBuilder[Type]) WithNamespace(namespace string) *resourceBuilder[Type] {
	r.obj.SetNamespace(namespace)
	return r
}

func (r *resourceBuilder[Type]) WithResourceVersion(resourceVersion string) *resourceBuilder[Type] {
	r.obj.SetResourceVersion(resourceVersion)
	return r
}

func (r *resourceBuilder[Type]) WithDeletionTimestamp() *resourceBuilder[Type] {
	now := metav1.Now()
	r.obj.SetDeletionTimestamp(&now)
	return r
}

func (r *resourceBuilder[Type]) WithClusterName(clusterName string) *resourceBuilder[Type] {
	annotations := r.obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[logicalcluster.AnnotationKey] = clusterName

	r.obj.SetAnnotations(annotations)
	return r
}

func (r *resourceBuilder[Type]) WithAnnotations(additionalAnnotations map[string]string) *resourceBuilder[Type] {
	annotations := r.obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	for k, v := range additionalAnnotations {
		annotations[k] = v
	}

	r.obj.SetAnnotations(annotations)
	return r
}

func (r *resourceBuilder[Type]) WithLabels(additionalLabels map[string]string) *resourceBuilder[Type] {
	labels := r.obj.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	for k, v := range additionalLabels {
		labels[k] = v
	}

	r.obj.SetLabels(labels)
	return r
}

func (r *resourceBuilder[Type]) WithFinalizers(finalizers ...string) *resourceBuilder[Type] {
	r.obj.SetFinalizers(finalizers)
	return r
}

func newResourceBuilder[Type metav1.Object](obj Type, name string) *resourceBuilder[Type] {
	obj.SetName(name)
	return &resourceBuilder[Type]{obj}
}

func namespace(name string) *resourceBuilder[*corev1.Namespace] {
	return newResourceBuilder(&corev1.Namespace{}, name)
}

func pv(name string) *resourceBuilder[*corev1.PersistentVolume] {
	return newResourceBuilder(&corev1.PersistentVolume{}, name)
}

func pod(name string) *resourceBuilder[*corev1.Pod] {
	return newResourceBuilder(&corev1.Pod{}, name)
}
