/*
Copyright The KCP Authors.

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

// Code generated by kcp code-generator. DO NOT EDIT.

package fake

import (
	"github.com/kcp-dev/logicalcluster/v3"

	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	"k8s.io/client-go/rest"

	kcptopologyv1alpha1 "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster/typed/topology/v1alpha1"
	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/topology/v1alpha1"
)

var _ kcptopologyv1alpha1.TopologyV1alpha1ClusterInterface = (*TopologyV1alpha1ClusterClient)(nil)

type TopologyV1alpha1ClusterClient struct {
	*kcptesting.Fake
}

func (c *TopologyV1alpha1ClusterClient) Cluster(clusterPath logicalcluster.Path) topologyv1alpha1.TopologyV1alpha1Interface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return &TopologyV1alpha1Client{Fake: c.Fake, ClusterPath: clusterPath}
}

func (c *TopologyV1alpha1ClusterClient) Partitions() kcptopologyv1alpha1.PartitionClusterInterface {
	return &partitionsClusterClient{Fake: c.Fake}
}

func (c *TopologyV1alpha1ClusterClient) PartitionSets() kcptopologyv1alpha1.PartitionSetClusterInterface {
	return &partitionSetsClusterClient{Fake: c.Fake}
}

var _ topologyv1alpha1.TopologyV1alpha1Interface = (*TopologyV1alpha1Client)(nil)

type TopologyV1alpha1Client struct {
	*kcptesting.Fake
	ClusterPath logicalcluster.Path
}

func (c *TopologyV1alpha1Client) RESTClient() rest.Interface {
	var ret *rest.RESTClient
	return ret
}

func (c *TopologyV1alpha1Client) Partitions() topologyv1alpha1.PartitionInterface {
	return &partitionsClient{Fake: c.Fake, ClusterPath: c.ClusterPath}
}

func (c *TopologyV1alpha1Client) PartitionSets() topologyv1alpha1.PartitionSetInterface {
	return &partitionSetsClient{Fake: c.Fake, ClusterPath: c.ClusterPath}
}
