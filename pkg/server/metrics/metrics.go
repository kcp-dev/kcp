/*
Copyright 2025 The KCP Authors.

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
	tenancyinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/tenancy/v1alpha1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
)

var (
	logicalClusterCount = metrics.NewGaugeVec(
		&metrics.GaugeOpts{
			Name:           "kcp_logicalcluster_count",
			Help:           "Number of logical clusters currently running on this shard.",
			StabilityLevel: metrics.ALPHA,
		},
		[]string{"shard"},
	)
)

func init() {
	legacyregistry.MustRegister(logicalClusterCount)
}

func UpdateLogicalClusterCount(informer tenancyinformers.WorkspaceClusterInformer, shardName string) {
	workspaces, err := informer.Lister().List(labels.Everything())
	if err != nil {
		return
	}
	count := len(workspaces)
	logicalClusterCount.WithLabelValues(shardName).Set(float64(count))
}
