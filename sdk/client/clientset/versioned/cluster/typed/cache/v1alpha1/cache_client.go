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

// Code generated by cluster-client-gen. DO NOT EDIT.

package v1alpha1

import (
	http "net/http"

	rest "k8s.io/client-go/rest"

	kcpclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"

	kcpcachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	kcpscheme "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster/scheme"
	kcpv1alpha1 "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/cache/v1alpha1"
)

type CacheV1alpha1ClusterInterface interface {
	CacheV1alpha1ClusterScoper
	CachedObjectsClusterGetter
	CachedResourcesClusterGetter
	CachedResourceEndpointSlicesClusterGetter
}

type CacheV1alpha1ClusterScoper interface {
	Cluster(logicalcluster.Path) kcpv1alpha1.CacheV1alpha1Interface
}

// CacheV1alpha1ClusterClient is used to interact with features provided by the cache.kcp.io group.
type CacheV1alpha1ClusterClient struct {
	clientCache kcpclient.Cache[*kcpv1alpha1.CacheV1alpha1Client]
}

func (c *CacheV1alpha1ClusterClient) Cluster(clusterPath logicalcluster.Path) kcpv1alpha1.CacheV1alpha1Interface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return c.clientCache.ClusterOrDie(clusterPath)
}

func (c *CacheV1alpha1ClusterClient) CachedObjects() CachedObjectClusterInterface {
	return &cachedObjectsClusterInterface{clientCache: c.clientCache}
}

func (c *CacheV1alpha1ClusterClient) CachedResources() CachedResourceClusterInterface {
	return &cachedResourcesClusterInterface{clientCache: c.clientCache}
}

func (c *CacheV1alpha1ClusterClient) CachedResourceEndpointSlices() CachedResourceEndpointSliceClusterInterface {
	return &cachedResourceEndpointSlicesClusterInterface{clientCache: c.clientCache}
}

// NewForConfig creates a new CacheV1alpha1ClusterClient for the given config.
// NewForConfig is equivalent to NewForConfigAndClient(c, httpClient),
// where httpClient was generated with rest.HTTPClientFor(c).
func NewForConfig(c *rest.Config) (*CacheV1alpha1ClusterClient, error) {
	config := *c
	setConfigDefaults(&config)
	httpClient, err := rest.HTTPClientFor(&config)
	if err != nil {
		return nil, err
	}
	return NewForConfigAndClient(&config, httpClient)
}

// NewForConfigAndClient creates a new CacheV1alpha1ClusterClient for the given config and http client.
// Note the http client provided takes precedence over the configured transport values.
func NewForConfigAndClient(c *rest.Config, h *http.Client) (*CacheV1alpha1ClusterClient, error) {
	cache := kcpclient.NewCache(c, h, &kcpclient.Constructor[*kcpv1alpha1.CacheV1alpha1Client]{
		NewForConfigAndClient: kcpv1alpha1.NewForConfigAndClient,
	})
	if _, err := cache.Cluster(logicalcluster.Name("root").Path()); err != nil {
		return nil, err
	}

	return &CacheV1alpha1ClusterClient{clientCache: cache}, nil
}

// NewForConfigOrDie creates a new CacheV1alpha1ClusterClient for the given config and
// panics if there is an error in the config.
func NewForConfigOrDie(c *rest.Config) *CacheV1alpha1ClusterClient {
	client, err := NewForConfig(c)
	if err != nil {
		panic(err)
	}
	return client
}

func setConfigDefaults(config *rest.Config) {
	gv := kcpcachev1alpha1.SchemeGroupVersion
	config.GroupVersion = &gv
	config.APIPath = "/apis"
	config.NegotiatedSerializer = rest.CodecFactoryForGeneratedClient(kcpscheme.Scheme, kcpscheme.Codecs).WithoutConversion()

	if config.UserAgent == "" {
		config.UserAgent = rest.DefaultKubernetesUserAgent()
	}
}
