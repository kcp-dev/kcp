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

	kcptenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster/typed/tenancy/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/tenancy/v1alpha1"
)

var _ kcptenancyv1alpha1.TenancyV1alpha1ClusterInterface = (*TenancyV1alpha1ClusterClient)(nil)

type TenancyV1alpha1ClusterClient struct {
	*kcptesting.Fake
}

func (c *TenancyV1alpha1ClusterClient) Cluster(clusterPath logicalcluster.Path) tenancyv1alpha1.TenancyV1alpha1Interface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return &TenancyV1alpha1Client{Fake: c.Fake, ClusterPath: clusterPath}
}

func (c *TenancyV1alpha1ClusterClient) Workspaces() kcptenancyv1alpha1.WorkspaceClusterInterface {
	return &workspacesClusterClient{Fake: c.Fake}
}

func (c *TenancyV1alpha1ClusterClient) WorkspaceTypes() kcptenancyv1alpha1.WorkspaceTypeClusterInterface {
	return &workspaceTypesClusterClient{Fake: c.Fake}
}

var _ tenancyv1alpha1.TenancyV1alpha1Interface = (*TenancyV1alpha1Client)(nil)

type TenancyV1alpha1Client struct {
	*kcptesting.Fake
	ClusterPath logicalcluster.Path
}

func (c *TenancyV1alpha1Client) RESTClient() rest.Interface {
	var ret *rest.RESTClient
	return ret
}

func (c *TenancyV1alpha1Client) Workspaces() tenancyv1alpha1.WorkspaceInterface {
	return &workspacesClient{Fake: c.Fake, ClusterPath: c.ClusterPath}
}

func (c *TenancyV1alpha1Client) WorkspaceTypes() tenancyv1alpha1.WorkspaceTypeInterface {
	return &workspaceTypesClient{Fake: c.Fake, ClusterPath: c.ClusterPath}
}
