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
	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/client-go/rest"
	kcpapisv1alpha2 "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster/typed/apis/v1alpha2"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/apis/v1alpha2"
)

var _ kcpapisv1alpha2.ApisV1alpha2ClusterInterface = (*ApisV1alpha2ClusterClient)(nil)

type ApisV1alpha2ClusterClient struct {
	*kcptesting.Fake 
}

func (c *ApisV1alpha2ClusterClient) Cluster(clusterPath logicalcluster.Path) apisv1alpha2.ApisV1alpha2Interface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return &ApisV1alpha2Client{Fake: c.Fake, ClusterPath: clusterPath}
}


func (c *ApisV1alpha2ClusterClient) APIExports() kcpapisv1alpha2.APIExportClusterInterface {
	return &aPIExportsClusterClient{Fake: c.Fake}
}
var _ apisv1alpha2.ApisV1alpha2Interface = (*ApisV1alpha2Client)(nil)

type ApisV1alpha2Client struct {
	*kcptesting.Fake
	ClusterPath logicalcluster.Path
}

func (c *ApisV1alpha2Client) RESTClient() rest.Interface {
	var ret *rest.RESTClient
	return ret
}


func (c *ApisV1alpha2Client) APIExports() apisv1alpha2.APIExportInterface {
	return &aPIExportsClient{Fake: c.Fake, ClusterPath: c.ClusterPath}
}
