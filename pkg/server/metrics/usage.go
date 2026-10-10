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
	"sync"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/component-base/metrics"
	"k8s.io/klog/v2"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"
	tenancyv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/tenancy/v1alpha1"
)

var (
	logicalClusterCountDesc = metrics.NewDesc(
		"kcp_logicalcluster_count",
		"Number of logical clusters currently running with specific phases on this shard.",
		[]string{"shard", "phase"},
		nil,
		metrics.ALPHA,
		"",
	)

	workspaceCountDesc = metrics.NewDesc(
		"kcp_workspace_count",
		"Number of workspaces currently running with specific phases on this shard.",
		[]string{"shard", "phase"},
		nil,
		metrics.ALPHA,
		"",
	)

	apiBindingPhaseDesc = metrics.NewDesc(
		"kcp_apibinding_phase",
		"Number of APIBindings in each phase (Binding, Bound).",
		[]string{"shard", "phase"},
		nil,
		metrics.ALPHA,
		"",
	)

	apiBindingConditionStatusDesc = metrics.NewDesc(
		"kcp_apibinding_condition_status",
		"Number of APIBindings with each condition type and status (True, False, Unknown).",
		[]string{"shard", "condition", "status"},
		nil,
		metrics.ALPHA,
		"",
	)

	apiExportConditionStatusDesc = metrics.NewDesc(
		"kcp_apiexport_condition_status",
		"Number of APIExports with each condition type and status (True, False, Unknown).",
		[]string{"shard", "condition", "status"},
		nil,
		metrics.ALPHA,
		"",
	)

	// logicalClusterPhases are the phases of LogicalClusters and Workspaces
	// that are always reported, even if no object is in them, so that their
	// series do not appear and disappear as objects move between phases.
	logicalClusterPhases = []corev1alpha1.LogicalClusterPhaseType{
		corev1alpha1.LogicalClusterPhaseScheduling,
		corev1alpha1.LogicalClusterPhaseInitializing,
		corev1alpha1.LogicalClusterPhaseReady,
		corev1alpha1.LogicalClusterPhaseUnavailable,
		corev1alpha1.LogicalClusterPhaseInactive,
		corev1alpha1.LogicalClusterPhaseTerminating,
		corev1alpha1.LogicalClusterPhaseDeleting,
	}

	// apiBindingPhases are the APIBinding phases that are always reported.
	apiBindingPhases = []apisv1alpha2.APIBindingPhaseType{
		apisv1alpha2.APIBindingPhaseBinding,
		apisv1alpha2.APIBindingPhaseBound,
	}

	usage = newUsageCollector()
)

// RegisterUsageInformers makes the usage gauges of the given shard
// (kcp_workspace_count, kcp_logicalcluster_count, kcp_apibinding_phase,
// kcp_apibinding_condition_status and kcp_apiexport_condition_status) report
// the objects held by the given shard-local informers.
//
// The gauges are computed from the informer caches whenever metrics are
// collected. Unlike counters maintained from event handlers, they cannot
// drift away from the actual number of objects through missed or replayed
// events, or through controllers being reinstalled after a leader election,
// and every replica of a shard reports the same values.
//
// It must be called before the informers are started. Calling it again for
// the same shard replaces the previously registered informers.
func RegisterUsageInformers(
	shardName string,
	workspaceInformer tenancyv1alpha1informers.WorkspaceClusterInformer,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
) {
	usage.register(shardName, &usageInformers{
		workspaces:      workspaceInformer,
		logicalClusters: logicalClusterInformer,
		apiBindings:     apiBindingInformer,
		apiExports:      apiExportInformer,
	})
}

type usageInformers struct {
	workspaces      tenancyv1alpha1informers.WorkspaceClusterInformer
	logicalClusters corev1alpha1informers.LogicalClusterClusterInformer
	apiBindings     apisv1alpha2informers.APIBindingClusterInformer
	apiExports      apisv1alpha2informers.APIExportClusterInformer
}

// usageCollector computes the usage gauges of every registered shard from
// the shard's informer caches at collection time.
type usageCollector struct {
	metrics.BaseStableCollector

	lock   sync.RWMutex
	shards map[string]*usageInformers
}

var _ metrics.StableCollector = &usageCollector{}

func newUsageCollector() *usageCollector {
	return &usageCollector{
		shards: map[string]*usageInformers{},
	}
}

func (c *usageCollector) register(shardName string, informers *usageInformers) {
	// Request the informers from their factories now, so that they are
	// started together with all the other informers of the shard.
	informers.workspaces.Informer()
	informers.logicalClusters.Informer()
	informers.apiBindings.Informer()
	informers.apiExports.Informer()

	c.lock.Lock()
	defer c.lock.Unlock()
	c.shards[shardName] = informers
}

// DescribeWithStability implements metrics.StableCollector.
func (c *usageCollector) DescribeWithStability(ch chan<- *metrics.Desc) {
	ch <- logicalClusterCountDesc
	ch <- workspaceCountDesc
	ch <- apiBindingPhaseDesc
	ch <- apiBindingConditionStatusDesc
	ch <- apiExportConditionStatusDesc
}

// CollectWithStability implements metrics.StableCollector.
func (c *usageCollector) CollectWithStability(ch chan<- metrics.Metric) {
	c.lock.RLock()
	defer c.lock.RUnlock()

	for shardName, informers := range c.shards {
		informers.collect(shardName, ch)
	}
}

// collect emits the usage gauges of a single shard. Gauges of informers that
// have not synced yet are skipped, as a partial cache would under-report.
func (i *usageInformers) collect(shardName string, ch chan<- metrics.Metric) {
	logger := klog.Background().WithValues("shard", shardName)

	if i.logicalClusters.Informer().HasSynced() {
		if logicalClusters, err := i.logicalClusters.Lister().List(labels.Everything()); err != nil {
			logger.Error(err, "failed to list LogicalClusters for usage metrics")
		} else {
			counts := zeroCounts(logicalClusterPhases)
			for _, logicalCluster := range logicalClusters {
				counts[string(logicalCluster.Status.Phase)]++
			}
			emitPhaseCounts(ch, logicalClusterCountDesc, shardName, counts)
		}
	}

	if i.workspaces.Informer().HasSynced() {
		if workspaces, err := i.workspaces.Lister().List(labels.Everything()); err != nil {
			logger.Error(err, "failed to list Workspaces for usage metrics")
		} else {
			counts := zeroCounts(logicalClusterPhases)
			for _, workspace := range workspaces {
				counts[string(workspace.Status.Phase)]++
			}
			emitPhaseCounts(ch, workspaceCountDesc, shardName, counts)
		}
	}

	if i.apiBindings.Informer().HasSynced() {
		if apiBindings, err := i.apiBindings.Lister().List(labels.Everything()); err != nil {
			logger.Error(err, "failed to list APIBindings for usage metrics")
		} else {
			phaseCounts := zeroCounts(apiBindingPhases)
			conditionCounts := map[conditionStatus]int{}
			for _, apiBinding := range apiBindings {
				phaseCounts[string(apiBinding.Status.Phase)]++
				countConditions(conditionCounts, apiBinding.Status.Conditions)
			}
			emitPhaseCounts(ch, apiBindingPhaseDesc, shardName, phaseCounts)
			emitConditionCounts(ch, apiBindingConditionStatusDesc, shardName, conditionCounts)
		}
	}

	if i.apiExports.Informer().HasSynced() {
		if apiExports, err := i.apiExports.Lister().List(labels.Everything()); err != nil {
			logger.Error(err, "failed to list APIExports for usage metrics")
		} else {
			conditionCounts := map[conditionStatus]int{}
			for _, apiExport := range apiExports {
				countConditions(conditionCounts, apiExport.Status.Conditions)
			}
			emitConditionCounts(ch, apiExportConditionStatusDesc, shardName, conditionCounts)
		}
	}
}

// zeroCounts returns per-phase counts that start at zero for the given phases.
func zeroCounts[T ~string](phases []T) map[string]int {
	counts := make(map[string]int, len(phases))
	for _, phase := range phases {
		counts[string(phase)] = 0
	}
	return counts
}

// conditionStatus is a condition type together with one of its statuses.
type conditionStatus struct {
	conditionType string
	status        string
}

func countConditions(counts map[conditionStatus]int, conditions conditionsv1alpha1.Conditions) {
	for _, condition := range conditions {
		counts[conditionStatus{conditionType: string(condition.Type), status: string(condition.Status)}]++
	}
}

// emitPhaseCounts emits one sample per phase. Objects without a phase have
// not been processed by their controller yet and are not reported.
func emitPhaseCounts(ch chan<- metrics.Metric, desc *metrics.Desc, shardName string, counts map[string]int) {
	for phase, count := range counts {
		if phase == "" {
			continue
		}
		ch <- metrics.NewLazyConstMetric(desc, metrics.GaugeValue, float64(count), shardName, phase)
	}
}

func emitConditionCounts(ch chan<- metrics.Metric, desc *metrics.Desc, shardName string, counts map[conditionStatus]int) {
	for condition, count := range counts {
		ch <- metrics.NewLazyConstMetric(desc, metrics.GaugeValue, float64(count), shardName, condition.conditionType, condition.status)
	}
}
