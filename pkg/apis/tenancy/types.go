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

package tenancy

import (
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/klog/v2"
)

// Cluster is a logical cluster name, not a workspace path.
//
// +kubebuilder:validation:Pattern:="^[a-z0-9]([-:a-z0-9]*[a-z0-9])?$"
type Cluster string

func (c Cluster) LogicalCluster() logicalcluster.Name {
	return logicalcluster.New(string(c))
}

// TemporaryCanonicalPath maps a cluster name to the canonical workspace path
// for that cluster. This is temporary, and it will be replaced by some cached
// mapping backed by the workspace index, probably of the front-proxy.
func (c Cluster) TemporaryCanonicalPath() logicalcluster.Name {
	logger := klog.Background()
	logger.V(1).Info("TemporaryCanonicalPath", "cluster", c) // intentionally noisy output
	return logicalcluster.New(string(c))
}

// TemporaryClusterFrom returns the cluster name for a given workspace path.
// This is temporary, and it will be replaced by some cached mapping backed
// by the workspace index, probably of the front-proxy.
func TemporaryClusterFrom(path logicalcluster.Name) Cluster {
	logger := klog.Background()
	logger.V(1).Info("TemporaryClusterFrom", "path", path) // intentionally noisy output
	return Cluster(path.String())
}
