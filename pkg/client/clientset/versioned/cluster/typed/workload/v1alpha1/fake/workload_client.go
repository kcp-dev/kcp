//go:build !ignore_autogenerated
// +build !ignore_autogenerated

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

package v1alpha1

import (
	"github.com/kcp-dev/logicalcluster/v3"

	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	"k8s.io/client-go/rest"

	kcpworkloadv1alpha1 "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster/typed/workload/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/workload/v1alpha1"
)

var _ kcpworkloadv1alpha1.WorkloadV1alpha1ClusterInterface = (*WorkloadV1alpha1ClusterClient)(nil)

type WorkloadV1alpha1ClusterClient struct {
	*kcptesting.Fake
}

func (c *WorkloadV1alpha1ClusterClient) Cluster(clusterPath logicalcluster.Path) workloadv1alpha1.WorkloadV1alpha1Interface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return &WorkloadV1alpha1Client{Fake: c.Fake, ClusterPath: clusterPath}
}

func (c *WorkloadV1alpha1ClusterClient) SyncTargets() kcpworkloadv1alpha1.SyncTargetClusterInterface {
	return &syncTargetsClusterClient{Fake: c.Fake}
}

var _ workloadv1alpha1.WorkloadV1alpha1Interface = (*WorkloadV1alpha1Client)(nil)

type WorkloadV1alpha1Client struct {
	*kcptesting.Fake
	ClusterPath logicalcluster.Path
}

func (c *WorkloadV1alpha1Client) RESTClient() rest.Interface {
	var ret *rest.RESTClient
	return ret
}

func (c *WorkloadV1alpha1Client) SyncTargets() workloadv1alpha1.SyncTargetInterface {
	return &syncTargetsClient{Fake: c.Fake, ClusterPath: c.ClusterPath}
}
