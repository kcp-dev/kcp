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
	apiBindingReadyDurationMs = metrics.NewHistogramVec(
		&metrics.HistogramOpts{
			Name:           "kcp_apibinding_ready_duration_ms",
			Help:           "Duration in milliseconds from APIBinding creation to reaching the Bound phase.",
			StabilityLevel: metrics.ALPHA,
			Buckets:        []float64{100, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000, 300000},
		},
		[]string{"shard"},
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

	// logicalClusterObjectCount and logicalClusterObjectLimit are only
	// published for logical clusters at or above 90% of their total object
	// count limit to keep the label cardinality bounded.
	logicalClusterObjectCount = metrics.NewGaugeVec(
		&metrics.GaugeOpts{
			Name:           "kcp_logicalcluster_object_count",
			Help:           "Total number of objects in a logical cluster. Only published for logical clusters at or above 90% of their total object count limit.",
			StabilityLevel: metrics.ALPHA,
		},
		[]string{"shard", "cluster"},
	)

	logicalClusterObjectLimit = metrics.NewGaugeVec(
		&metrics.GaugeOpts{
			Name:           "kcp_logicalcluster_object_limit",
			Help:           "Effective total object count limit of a logical cluster. Only published for logical clusters at or above 90% of their total object count limit.",
			StabilityLevel: metrics.ALPHA,
		},
		[]string{"shard", "cluster"},
	)
)

func init() {
	legacyregistry.CustomMustRegister(usage)
	legacyregistry.MustRegister(apiBindingReadyDurationMs)
	legacyregistry.MustRegister(apiExportReadyDurationMs)
	legacyregistry.MustRegister(logicalClusterObjectCount)
	legacyregistry.MustRegister(logicalClusterObjectLimit)
}

// SetLogicalClusterObjectCount publishes the total object count and effective
// limit for the given logical cluster.
func SetLogicalClusterObjectCount(shardName, cluster string, count, limit int64) {
	logicalClusterObjectCount.WithLabelValues(shardName, cluster).Set(float64(count))
	logicalClusterObjectLimit.WithLabelValues(shardName, cluster).Set(float64(limit))
}

// DeleteLogicalClusterObjectCount stops publishing object count metrics for
// the given logical cluster.
func DeleteLogicalClusterObjectCount(shardName, cluster string) {
	logicalClusterObjectCount.DeleteLabelValues(shardName, cluster)
	logicalClusterObjectLimit.DeleteLabelValues(shardName, cluster)
}

// ObserveAPIBindingReadyDuration records the duration from creation to Bound phase.
func ObserveAPIBindingReadyDuration(shardName string, creationTime time.Time) {
	apiBindingReadyDurationMs.WithLabelValues(shardName).Observe(float64(time.Since(creationTime).Milliseconds()))
}

// ObserveAPIExportReadyDuration records the duration from APIExport creation to fully operational.
func ObserveAPIExportReadyDuration(shardName string, creationTime time.Time) {
	apiExportReadyDurationMs.WithLabelValues(shardName).Observe(float64(time.Since(creationTime).Milliseconds()))
}
