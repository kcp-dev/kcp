/*
Copyright 2025 The kcp Authors.

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
	"time"

	"k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
)

var (
	logicalClusterCount = metrics.NewGaugeVec(
		&metrics.GaugeOpts{
			Name:           "kcp_logicalcluster_count",
			Help:           "Number of logical clusters currently running with specific phases on this shard.",
			StabilityLevel: metrics.ALPHA,
		},
		[]string{"shard", "phase"},
	)

	workspaceCount = metrics.NewGaugeVec(
		&metrics.GaugeOpts{
			Name:           "kcp_workspace_count",
			Help:           "Number of workspaces currently running with specific phases on this shard.",
			StabilityLevel: metrics.ALPHA,
		},
		[]string{"shard", "phase"},
	)

	apiBindingPhase = metrics.NewGaugeVec(
		&metrics.GaugeOpts{
			Name:           "kcp_apibinding_phase",
			Help:           "Number of APIBindings in each phase (Binding, Bound, or empty for newly created).",
			StabilityLevel: metrics.ALPHA,
		},
		[]string{"shard", "phase"},
	)

	apiBindingConditionStatus = metrics.NewGaugeVec(
		&metrics.GaugeOpts{
			Name:           "kcp_apibinding_condition_status",
			Help:           "Number of APIBindings with each condition type and status (True, False, Unknown).",
			StabilityLevel: metrics.ALPHA,
		},
		[]string{"shard", "condition", "status"},
	)

	apiBindingReadyDurationMs = metrics.NewHistogramVec(
		&metrics.HistogramOpts{
			Name:           "kcp_apibinding_ready_duration_ms",
			Help:           "Duration in milliseconds from APIBinding creation to reaching the Bound phase.",
			StabilityLevel: metrics.ALPHA,
			Buckets:        []float64{100, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000, 300000},
		},
		[]string{"shard"},
	)

	apiExportConditionStatus = metrics.NewGaugeVec(
		&metrics.GaugeOpts{
			Name:           "kcp_apiexport_condition_status",
			Help:           "Number of APIExports with each condition type and status (True, False, Unknown).",
			StabilityLevel: metrics.ALPHA,
		},
		[]string{"shard", "condition", "status"},
	)

	apiExportReadyDurationMs = metrics.NewHistogramVec(
		&metrics.HistogramOpts{
			Name:           "kcp_apiexport_ready_duration_ms",
			Help:           "Duration in milliseconds from APIExport creation to becoming fully operational (IdentityValid and VirtualWorkspaceURLsReady both True).",
			StabilityLevel: metrics.ALPHA,
			Buckets:        []float64{100, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000, 300000},
		},
		[]string{"shard"},
	)
)

func init() {
	legacyregistry.MustRegister(logicalClusterCount)
	legacyregistry.MustRegister(workspaceCount)
	legacyregistry.MustRegister(apiBindingPhase)
	legacyregistry.MustRegister(apiBindingConditionStatus)
	legacyregistry.MustRegister(apiBindingReadyDurationMs)
	legacyregistry.MustRegister(apiExportConditionStatus)
	legacyregistry.MustRegister(apiExportReadyDurationMs)
}

// IncrementLogicalClusterCount increments the count for the given shard and phase.
func IncrementLogicalClusterCount(shardName string, phase string) {
	logicalClusterCount.WithLabelValues(shardName, phase).Inc()
}

// DecrementLogicalClusterCount decrements the count for the given shard and phase.
func DecrementLogicalClusterCount(shardName string, phase string) {
	logicalClusterCount.WithLabelValues(shardName, phase).Dec()
}

// IncrementWorkspaceCount increments the count for the given shard and phase.
func IncrementWorkspaceCount(shardName string, phase string) {
	workspaceCount.WithLabelValues(shardName, phase).Inc()
}

// DecrementWorkspaceCount decrements the count for the given shard and phase.
func DecrementWorkspaceCount(shardName string, phase string) {
	workspaceCount.WithLabelValues(shardName, phase).Dec()
}

// IncrementAPIBindingPhase increments the gauge for the given APIBinding phase.
func IncrementAPIBindingPhase(shardName, phase string) {
	apiBindingPhase.WithLabelValues(shardName, phase).Inc()
}

// DecrementAPIBindingPhase decrements the gauge for the given APIBinding phase.
func DecrementAPIBindingPhase(shardName, phase string) {
	apiBindingPhase.WithLabelValues(shardName, phase).Dec()
}

// IncrementAPIBindingConditionStatus increments the gauge for the given condition type and status.
func IncrementAPIBindingConditionStatus(shardName, conditionType, status string) {
	apiBindingConditionStatus.WithLabelValues(shardName, conditionType, status).Inc()
}

// DecrementAPIBindingConditionStatus decrements the gauge for the given condition type and status.
func DecrementAPIBindingConditionStatus(shardName, conditionType, status string) {
	apiBindingConditionStatus.WithLabelValues(shardName, conditionType, status).Dec()
}

// ObserveAPIBindingReadyDuration records the duration from creation to Bound phase.
func ObserveAPIBindingReadyDuration(shardName string, creationTime time.Time) {
	apiBindingReadyDurationMs.WithLabelValues(shardName).Observe(float64(time.Since(creationTime).Milliseconds()))
}

// IncrementAPIExportConditionStatus increments the gauge for the given APIExport condition type and status.
func IncrementAPIExportConditionStatus(shardName, conditionType, status string) {
	apiExportConditionStatus.WithLabelValues(shardName, conditionType, status).Inc()
}

// DecrementAPIExportConditionStatus decrements the gauge for the given APIExport condition type and status.
func DecrementAPIExportConditionStatus(shardName, conditionType, status string) {
	apiExportConditionStatus.WithLabelValues(shardName, conditionType, status).Dec()
}

// ObserveAPIExportReadyDuration records the duration from APIExport creation to fully operational.
func ObserveAPIExportReadyDuration(shardName string, creationTime time.Time) {
	apiExportReadyDurationMs.WithLabelValues(shardName).Observe(float64(time.Since(creationTime).Milliseconds()))
}
