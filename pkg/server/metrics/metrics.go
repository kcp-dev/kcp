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
)

func init() {
	legacyregistry.MustRegister(logicalClusterCount)
}

// IncrementLogicalClusterCount increments the count for the given shard and phase.
func IncrementLogicalClusterCount(shardName string, phase string) {
	logicalClusterCount.WithLabelValues(shardName, phase).Inc()
}

// DecrementLogicalClusterCount decrements the count for the given shard and phase.
func DecrementLogicalClusterCount(shardName string, phase string) {
	logicalClusterCount.WithLabelValues(shardName, phase).Dec()
}
