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

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/apis/v1alpha1"
)

type ApisV1alpha1ClusterInterface interface {
	ApisV1alpha1ClusterScoper
	APIBindingsClusterGetter
	APIExportsClusterGetter
	APIExportEndpointSlicesClusterGetter
	APIResourceSchemasClusterGetter
	APIConversionsClusterGetter
}

type ApisV1alpha1ClusterScoper interface {
	Cluster(logicalcluster.Path) apisv1alpha1.ApisV1alpha1Interface
}

type ApisV1alpha1ClusterClient struct {
	clientCache kcpclient.Cache[*apisv1alpha1.ApisV1alpha1Client]
}

func (c *ApisV1alpha1ClusterClient) Cluster(clusterPath logicalcluster.Path) apisv1alpha1.ApisV1alpha1Interface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return c.clientCache.ClusterOrDie(clusterPath)
}


func (c *ApisV1alpha1ClusterClient) APIBindings() APIBindingClusterInterface {
	return &aPIBindingsClusterInterface{clientCache: c.clientCache}
}

func (c *ApisV1alpha1ClusterClient) APIExports() APIExportClusterInterface {
	return &aPIExportsClusterInterface{clientCache: c.clientCache}
}

func (c *ApisV1alpha1ClusterClient) APIExportEndpointSlices() APIExportEndpointSliceClusterInterface {
	return &aPIExportEndpointSlicesClusterInterface{clientCache: c.clientCache}
}

func (c *ApisV1alpha1ClusterClient) APIResourceSchemas() APIResourceSchemaClusterInterface {
	return &aPIResourceSchemasClusterInterface{clientCache: c.clientCache}
}

func (c *ApisV1alpha1ClusterClient) APIConversions() APIConversionClusterInterface {
	return &aPIConversionsClusterInterface{clientCache: c.clientCache}
}
// NewForConfig creates a new ApisV1alpha1ClusterClient for the given config.
// NewForConfig is equivalent to NewForConfigAndClient(c, httpClient),
// where httpClient was generated with rest.HTTPClientFor(c).
func NewForConfig(c *rest.Config) (*ApisV1alpha1ClusterClient, error) {
	client, err := rest.HTTPClientFor(c)
	if err != nil {
		return nil, err
	}
	return NewForConfigAndClient(c, client)
}

// NewForConfigAndClient creates a new ApisV1alpha1ClusterClient for the given config and http client.
// Note the http client provided takes precedence over the configured transport values.
func NewForConfigAndClient(c *rest.Config, h *http.Client) (*ApisV1alpha1ClusterClient, error) {
	cache := kcpclient.NewCache(c, h, &kcpclient.Constructor[*apisv1alpha1.ApisV1alpha1Client]{
		NewForConfigAndClient: apisv1alpha1.NewForConfigAndClient,
	})
	if _, err := cache.Cluster(logicalcluster.Name("root").Path()); err != nil {
		return nil, err
	}
	return &ApisV1alpha1ClusterClient{clientCache: cache}, nil
}

// NewForConfigOrDie creates a new ApisV1alpha1ClusterClient for the given config and
// panics if there is an error in the config.
func NewForConfigOrDie(c *rest.Config) *ApisV1alpha1ClusterClient {
	client, err := NewForConfig(c)
	if err != nil {
		panic(err)
	}
	return client
}
