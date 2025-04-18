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
	"net/http"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/client-go/rest"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/core/v1alpha1"
)

type CoreV1alpha1ClusterInterface interface {
	CoreV1alpha1ClusterScoper
	LogicalClustersClusterGetter
	ShardsClusterGetter
}

type CoreV1alpha1ClusterScoper interface {
	Cluster(logicalcluster.Path) corev1alpha1.CoreV1alpha1Interface
}

type CoreV1alpha1ClusterClient struct {
	clientCache kcpclient.Cache[*corev1alpha1.CoreV1alpha1Client]
}

func (c *CoreV1alpha1ClusterClient) Cluster(clusterPath logicalcluster.Path) corev1alpha1.CoreV1alpha1Interface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return c.clientCache.ClusterOrDie(clusterPath)
}


func (c *CoreV1alpha1ClusterClient) LogicalClusters() LogicalClusterClusterInterface {
	return &logicalClustersClusterInterface{clientCache: c.clientCache}
}

func (c *CoreV1alpha1ClusterClient) Shards() ShardClusterInterface {
	return &shardsClusterInterface{clientCache: c.clientCache}
}
// NewForConfig creates a new CoreV1alpha1ClusterClient for the given config.
// NewForConfig is equivalent to NewForConfigAndClient(c, httpClient),
// where httpClient was generated with rest.HTTPClientFor(c).
func NewForConfig(c *rest.Config) (*CoreV1alpha1ClusterClient, error) {
	client, err := rest.HTTPClientFor(c)
	if err != nil {
		return nil, err
	}
	return NewForConfigAndClient(c, client)
}

// NewForConfigAndClient creates a new CoreV1alpha1ClusterClient for the given config and http client.
// Note the http client provided takes precedence over the configured transport values.
func NewForConfigAndClient(c *rest.Config, h *http.Client) (*CoreV1alpha1ClusterClient, error) {
	cache := kcpclient.NewCache(c, h, &kcpclient.Constructor[*corev1alpha1.CoreV1alpha1Client]{
		NewForConfigAndClient: corev1alpha1.NewForConfigAndClient,
	})
	if _, err := cache.Cluster(logicalcluster.Name("root").Path()); err != nil {
		return nil, err
	}
	return &CoreV1alpha1ClusterClient{clientCache: cache}, nil
}

// NewForConfigOrDie creates a new CoreV1alpha1ClusterClient for the given config and
// panics if there is an error in the config.
func NewForConfigOrDie(c *rest.Config) *CoreV1alpha1ClusterClient {
	client, err := NewForConfig(c)
	if err != nil {
		panic(err)
	}
	return client
}
