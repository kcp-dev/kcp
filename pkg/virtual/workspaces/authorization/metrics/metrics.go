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

package metrics

import (
	"sync"

	"k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
)

// WorkspaceAuthorizerSubsystem - subsystem name used for workspace authorization.
const WorkspaceAuthorizerSubsystem = "workspace_authorizer"

var (
	// AuthorizationCacheSyncLatency tracks the duration of cache syncs, along with their result.
	AuthorizationCacheSyncLatency = metrics.NewHistogramVec(
		&metrics.HistogramOpts{
			Subsystem:      WorkspaceAuthorizerSubsystem,
			Name:           "sync_duration_seconds",
			Help:           "Cache sync latency in seconds.",
			StabilityLevel: metrics.ALPHA,
			Buckets:        metrics.ExponentialBuckets(0.0001, 2, 15),
		},
		[]string{
			"type",   // either "root" or "org"
			"result", // either "success", or "skip"
		},
	)

	// AuthorizationCaches tracks the number of authorization caches.
	AuthorizationCaches = metrics.NewCounterVec(
		&metrics.CounterOpts{
			Subsystem:      WorkspaceAuthorizerSubsystem,
			Name:           "caches",
			Help:           "Number of caches",
			StabilityLevel: metrics.ALPHA,
		},
		[]string{"type"}, // either "root" or "org"
	)

	// AuthorizationCacheInvalidations observes the number of cache invalidations.
	AuthorizationCacheInvalidations = metrics.NewCounterVec(
		&metrics.CounterOpts{
			Subsystem:      WorkspaceAuthorizerSubsystem,
			Name:           "invalidations",
			Help:           "Number of cache invalidations",
			StabilityLevel: metrics.ALPHA,
		},
		[]string{"type"}, // either "root" or "org"
	)

	metricsList = []metrics.Registerable{
		AuthorizationCacheSyncLatency,
		AuthorizationCaches,
		AuthorizationCacheInvalidations,
	}
)

var registerMetrics sync.Once

// Register registers authorization cache metrics.
func Register() {
	registerMetrics.Do(func() {
		RegisterMetrics(metricsList...)
	})
}

// RegisterMetrics registers a list of metrics.
// This function is exported because it is intended to be used by out-of-tree plugins to register their custom metrics.
func RegisterMetrics(extraMetrics ...metrics.Registerable) {
	for _, metric := range extraMetrics {
		legacyregistry.MustRegister(metric)
	}
}
