/*
Copyright 2026 The kcp Authors.

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

package metrics

import (
	"context"
	"strings"
	"testing"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/metrics"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	kcpfake "github.com/kcp-dev/sdk/client/clientset/versioned/cluster/fake"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
)

var usageMetricNames = []string{
	"kcp_logicalcluster_count",
	"kcp_workspace_count",
	"kcp_apibinding_phase",
	"kcp_apibinding_condition_status",
	"kcp_apiexport_condition_status",
}

// gatherAndCompare compares the metrics gathered from the registry with the
// expected text exposition. Unlike the component-base helpers it does not
// lint the metrics, as the names of the established kcp_workspace_count and
// kcp_logicalcluster_count gauges violate the "_count" suffix lint rule.
func gatherAndCompare(t *testing.T, registry metrics.KubeRegistry, expected string, metricNames ...string) {
	t.Helper()
	require.NoError(t, promtestutil.GatherAndCompare(registry, strings.NewReader(expected), metricNames...))
}

func objectMeta(cluster, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:        name,
		Annotations: map[string]string{logicalcluster.AnnotationKey: cluster},
	}
}

func condition(conditionType conditionsv1alpha1.ConditionType, status corev1.ConditionStatus) conditionsv1alpha1.Condition {
	return conditionsv1alpha1.Condition{Type: conditionType, Status: status}
}

// newUsageInformers returns the usage informers of a fake shard. When started
// is true, the informers are started and synced, and the given objects are
// added to their caches.
func newUsageInformers(t *testing.T, started bool, objects ...any) *usageInformers {
	t.Helper()

	factory := kcpinformers.NewSharedInformerFactory(kcpfake.NewClientset(), 0)
	informers := &usageInformers{
		workspaces:      factory.Tenancy().V1alpha1().Workspaces(),
		logicalClusters: factory.Core().V1alpha1().LogicalClusters(),
		apiBindings:     factory.Apis().V1alpha2().APIBindings(),
		apiExports:      factory.Apis().V1alpha2().APIExports(),
	}

	if !started {
		return informers
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	informers.workspaces.Informer()
	informers.logicalClusters.Informer()
	informers.apiBindings.Informer()
	informers.apiExports.Informer()
	factory.Start(ctx.Done())
	for typ, synced := range factory.WaitForCacheSync(ctx.Done()) {
		require.True(t, synced, "informer for %v did not sync", typ)
	}

	for _, obj := range objects {
		var err error
		switch obj := obj.(type) {
		case *tenancyv1alpha1.Workspace:
			err = informers.workspaces.Informer().GetIndexer().Add(obj)
		case *corev1alpha1.LogicalCluster:
			err = informers.logicalClusters.Informer().GetIndexer().Add(obj)
		case *apisv1alpha2.APIBinding:
			err = informers.apiBindings.Informer().GetIndexer().Add(obj)
		case *apisv1alpha2.APIExport:
			err = informers.apiExports.Informer().GetIndexer().Add(obj)
		default:
			t.Fatalf("unexpected object type %T", obj)
		}
		require.NoError(t, err)
	}

	return informers
}

func TestUsageCollector(t *testing.T) {
	t.Parallel()

	ready := corev1alpha1.LogicalClusterPhaseReady
	initializing := corev1alpha1.LogicalClusterPhaseInitializing

	objects := []any{
		&tenancyv1alpha1.Workspace{ObjectMeta: objectMeta("root", "ready-a"), Status: tenancyv1alpha1.WorkspaceStatus{Phase: ready}},
		&tenancyv1alpha1.Workspace{ObjectMeta: objectMeta("root:org", "ready-b"), Status: tenancyv1alpha1.WorkspaceStatus{Phase: ready}},
		&tenancyv1alpha1.Workspace{ObjectMeta: objectMeta("root", "initializing"), Status: tenancyv1alpha1.WorkspaceStatus{Phase: initializing}},
		&tenancyv1alpha1.Workspace{ObjectMeta: objectMeta("root", "new")},

		&corev1alpha1.LogicalCluster{ObjectMeta: objectMeta("a", corev1alpha1.LogicalClusterName), Status: corev1alpha1.LogicalClusterStatus{Phase: ready}},
		&corev1alpha1.LogicalCluster{ObjectMeta: objectMeta("b", corev1alpha1.LogicalClusterName), Status: corev1alpha1.LogicalClusterStatus{Phase: initializing}},
		&corev1alpha1.LogicalCluster{ObjectMeta: objectMeta("c", corev1alpha1.LogicalClusterName)},

		&apisv1alpha2.APIBinding{
			ObjectMeta: objectMeta("a", "bound"),
			Status: apisv1alpha2.APIBindingStatus{
				Phase: apisv1alpha2.APIBindingPhaseBound,
				Conditions: conditionsv1alpha1.Conditions{
					condition(conditionsv1alpha1.ReadyCondition, corev1.ConditionTrue),
					condition(apisv1alpha2.PermissionClaimsValid, corev1.ConditionTrue),
				},
			},
		},
		&apisv1alpha2.APIBinding{
			ObjectMeta: objectMeta("b", "bound"),
			Status: apisv1alpha2.APIBindingStatus{
				Phase: apisv1alpha2.APIBindingPhaseBound,
				Conditions: conditionsv1alpha1.Conditions{
					condition(conditionsv1alpha1.ReadyCondition, corev1.ConditionTrue),
					condition(apisv1alpha2.PermissionClaimsValid, corev1.ConditionFalse),
				},
			},
		},
		&apisv1alpha2.APIBinding{
			ObjectMeta: objectMeta("b", "binding"),
			Status: apisv1alpha2.APIBindingStatus{
				Phase: apisv1alpha2.APIBindingPhaseBinding,
				Conditions: conditionsv1alpha1.Conditions{
					condition(conditionsv1alpha1.ReadyCondition, corev1.ConditionFalse),
				},
			},
		},
		&apisv1alpha2.APIBinding{ObjectMeta: objectMeta("c", "new")},

		&apisv1alpha2.APIExport{
			ObjectMeta: objectMeta("a", "export"),
			Status: apisv1alpha2.APIExportStatus{
				Conditions: conditionsv1alpha1.Conditions{
					condition(apisv1alpha2.APIExportIdentityValid, corev1.ConditionTrue),
					condition(apisv1alpha2.APIExportVirtualWorkspaceURLsReady, corev1.ConditionUnknown),
				},
			},
		},
	}

	tests := map[string]struct {
		shards      func(t *testing.T) map[string]*usageInformers
		metricNames []string
		expected    string
	}{
		"objects are counted by phase and condition": {
			shards: func(t *testing.T) map[string]*usageInformers {
				t.Helper()
				return map[string]*usageInformers{"root": newUsageInformers(t, true, objects...)}
			},
			metricNames: usageMetricNames,
			expected: `
# HELP kcp_apibinding_condition_status [ALPHA] Number of APIBindings with each condition type and status (True, False, Unknown).
# TYPE kcp_apibinding_condition_status gauge
kcp_apibinding_condition_status{condition="PermissionClaimsValid",shard="root",status="False"} 1
kcp_apibinding_condition_status{condition="PermissionClaimsValid",shard="root",status="True"} 1
kcp_apibinding_condition_status{condition="Ready",shard="root",status="False"} 1
kcp_apibinding_condition_status{condition="Ready",shard="root",status="True"} 2
# HELP kcp_apibinding_phase [ALPHA] Number of APIBindings in each phase (Binding, Bound).
# TYPE kcp_apibinding_phase gauge
kcp_apibinding_phase{phase="Binding",shard="root"} 1
kcp_apibinding_phase{phase="Bound",shard="root"} 2
# HELP kcp_apiexport_condition_status [ALPHA] Number of APIExports with each condition type and status (True, False, Unknown).
# TYPE kcp_apiexport_condition_status gauge
kcp_apiexport_condition_status{condition="IdentityValid",shard="root",status="True"} 1
kcp_apiexport_condition_status{condition="VirtualWorkspaceURLsReady",shard="root",status="Unknown"} 1
# HELP kcp_logicalcluster_count [ALPHA] Number of logical clusters currently running with specific phases on this shard.
# TYPE kcp_logicalcluster_count gauge
kcp_logicalcluster_count{phase="Deleting",shard="root"} 0
kcp_logicalcluster_count{phase="Inactive",shard="root"} 0
kcp_logicalcluster_count{phase="Initializing",shard="root"} 1
kcp_logicalcluster_count{phase="Ready",shard="root"} 1
kcp_logicalcluster_count{phase="Scheduling",shard="root"} 0
kcp_logicalcluster_count{phase="Terminating",shard="root"} 0
kcp_logicalcluster_count{phase="Unavailable",shard="root"} 0
# HELP kcp_workspace_count [ALPHA] Number of workspaces currently running with specific phases on this shard.
# TYPE kcp_workspace_count gauge
kcp_workspace_count{phase="Deleting",shard="root"} 0
kcp_workspace_count{phase="Inactive",shard="root"} 0
kcp_workspace_count{phase="Initializing",shard="root"} 1
kcp_workspace_count{phase="Ready",shard="root"} 2
kcp_workspace_count{phase="Scheduling",shard="root"} 0
kcp_workspace_count{phase="Terminating",shard="root"} 0
kcp_workspace_count{phase="Unavailable",shard="root"} 0
`,
		},
		"informers that have not synced are not reported": {
			shards: func(t *testing.T) map[string]*usageInformers {
				t.Helper()
				return map[string]*usageInformers{"root": newUsageInformers(t, false)}
			},
			metricNames: usageMetricNames,
			expected:    "",
		},
		"every registered shard is reported": {
			shards: func(t *testing.T) map[string]*usageInformers {
				t.Helper()
				return map[string]*usageInformers{
					"root": newUsageInformers(t, true,
						&tenancyv1alpha1.Workspace{ObjectMeta: objectMeta("root", "a"), Status: tenancyv1alpha1.WorkspaceStatus{Phase: ready}},
					),
					"beta": newUsageInformers(t, true,
						&tenancyv1alpha1.Workspace{ObjectMeta: objectMeta("root:org", "b"), Status: tenancyv1alpha1.WorkspaceStatus{Phase: ready}},
						&tenancyv1alpha1.Workspace{ObjectMeta: objectMeta("root:org", "c"), Status: tenancyv1alpha1.WorkspaceStatus{Phase: ready}},
					),
				}
			},
			metricNames: []string{"kcp_workspace_count"},
			expected: `
# HELP kcp_workspace_count [ALPHA] Number of workspaces currently running with specific phases on this shard.
# TYPE kcp_workspace_count gauge
kcp_workspace_count{phase="Deleting",shard="beta"} 0
kcp_workspace_count{phase="Deleting",shard="root"} 0
kcp_workspace_count{phase="Inactive",shard="beta"} 0
kcp_workspace_count{phase="Inactive",shard="root"} 0
kcp_workspace_count{phase="Initializing",shard="beta"} 0
kcp_workspace_count{phase="Initializing",shard="root"} 0
kcp_workspace_count{phase="Ready",shard="beta"} 2
kcp_workspace_count{phase="Ready",shard="root"} 1
kcp_workspace_count{phase="Scheduling",shard="beta"} 0
kcp_workspace_count{phase="Scheduling",shard="root"} 0
kcp_workspace_count{phase="Terminating",shard="beta"} 0
kcp_workspace_count{phase="Terminating",shard="root"} 0
kcp_workspace_count{phase="Unavailable",shard="beta"} 0
kcp_workspace_count{phase="Unavailable",shard="root"} 0
`,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			collector := newUsageCollector()
			for shardName, informers := range tc.shards(t) {
				collector.register(shardName, informers)
			}
			registry := metrics.NewKubeRegistry()
			registry.CustomMustRegister(collector)

			gatherAndCompare(t, registry, tc.expected, tc.metricNames...)
		})
	}
}

func TestUsageCollectorReflectsCacheChanges(t *testing.T) {
	t.Parallel()

	informers := newUsageInformers(t, true)
	collector := newUsageCollector()
	collector.register("root", informers)
	registry := metrics.NewKubeRegistry()
	registry.CustomMustRegister(collector)

	workspace := &tenancyv1alpha1.Workspace{
		ObjectMeta: objectMeta("root", "ws"),
		Status:     tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseInitializing},
	}
	indexer := informers.workspaces.Informer().GetIndexer()

	expect := func(expected string) {
		t.Helper()
		gatherAndCompare(t, registry, expected, "kcp_workspace_count")
	}

	require.NoError(t, indexer.Add(workspace))
	expect(`
# HELP kcp_workspace_count [ALPHA] Number of workspaces currently running with specific phases on this shard.
# TYPE kcp_workspace_count gauge
kcp_workspace_count{phase="Deleting",shard="root"} 0
kcp_workspace_count{phase="Inactive",shard="root"} 0
kcp_workspace_count{phase="Initializing",shard="root"} 1
kcp_workspace_count{phase="Ready",shard="root"} 0
kcp_workspace_count{phase="Scheduling",shard="root"} 0
kcp_workspace_count{phase="Terminating",shard="root"} 0
kcp_workspace_count{phase="Unavailable",shard="root"} 0
`)

	// a replayed add of the same object must not be counted twice.
	require.NoError(t, indexer.Add(workspace))
	expect(`
# HELP kcp_workspace_count [ALPHA] Number of workspaces currently running with specific phases on this shard.
# TYPE kcp_workspace_count gauge
kcp_workspace_count{phase="Deleting",shard="root"} 0
kcp_workspace_count{phase="Inactive",shard="root"} 0
kcp_workspace_count{phase="Initializing",shard="root"} 1
kcp_workspace_count{phase="Ready",shard="root"} 0
kcp_workspace_count{phase="Scheduling",shard="root"} 0
kcp_workspace_count{phase="Terminating",shard="root"} 0
kcp_workspace_count{phase="Unavailable",shard="root"} 0
`)

	workspace = workspace.DeepCopy()
	workspace.Status.Phase = corev1alpha1.LogicalClusterPhaseReady
	require.NoError(t, indexer.Update(workspace))
	expect(`
# HELP kcp_workspace_count [ALPHA] Number of workspaces currently running with specific phases on this shard.
# TYPE kcp_workspace_count gauge
kcp_workspace_count{phase="Deleting",shard="root"} 0
kcp_workspace_count{phase="Inactive",shard="root"} 0
kcp_workspace_count{phase="Initializing",shard="root"} 0
kcp_workspace_count{phase="Ready",shard="root"} 1
kcp_workspace_count{phase="Scheduling",shard="root"} 0
kcp_workspace_count{phase="Terminating",shard="root"} 0
kcp_workspace_count{phase="Unavailable",shard="root"} 0
`)

	require.NoError(t, indexer.Delete(workspace))
	expect(`
# HELP kcp_workspace_count [ALPHA] Number of workspaces currently running with specific phases on this shard.
# TYPE kcp_workspace_count gauge
kcp_workspace_count{phase="Deleting",shard="root"} 0
kcp_workspace_count{phase="Inactive",shard="root"} 0
kcp_workspace_count{phase="Initializing",shard="root"} 0
kcp_workspace_count{phase="Ready",shard="root"} 0
kcp_workspace_count{phase="Scheduling",shard="root"} 0
kcp_workspace_count{phase="Terminating",shard="root"} 0
kcp_workspace_count{phase="Unavailable",shard="root"} 0
`)
}
