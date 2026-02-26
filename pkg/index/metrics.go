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

package index

import (
	"sync"

	compbasemetrics "k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
)

var (
	clustersOnShard = compbasemetrics.NewGaugeVec(
		&compbasemetrics.GaugeOpts{
			Namespace:      "kcp",
			Name:           "indexed_logicalclusters",
			Help:           "Number of logicalclusters indexed by kcp-front-proxy or embedded localproxy per shard.",
			StabilityLevel: compbasemetrics.ALPHA,
		},
		[]string{"shard"},
	)
)

var registerMetrics sync.Once

// Register metrics.
func Register() {
	registerMetrics.Do(func() {
		legacyregistry.MustRegister(clustersOnShard)
	})
}

func init() {
	Register()
}
