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

package delegated

import (
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	"k8s.io/apiserver/pkg/server/options"
	kubernetesclient "k8s.io/client-go/kubernetes"
	authorizationv1client "k8s.io/client-go/kubernetes/typed/authorization/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

type DelegatedAuthorizerFactory func(clusterName logicalcluster.Name, client kubernetesclient.ClusterInterface) (authorizer.Authorizer, error)

// NewDelegatedAuthorizer returns a new authorizer for use in e.g. admission plugins that delegates
// to the kube API server via SubjectAccessReview.
func NewDelegatedAuthorizer(clusterName logicalcluster.Name, client kubernetesclient.ClusterInterface) (authorizer.Authorizer, error) {
	delegatingAuthorizerConfig := &authorizerfactory.DelegatingAuthorizerConfig{
		SubjectAccessReviewClient: &clusterAwareAuthorizationV1Client{
			AuthorizationV1Interface: client.Cluster(clusterName).AuthorizationV1(),
			cluster:                  clusterName,
		},
		AllowCacheTTL:       5 * time.Minute,
		DenyCacheTTL:        30 * time.Second,
		WebhookRetryBackoff: options.DefaultAuthWebhookRetryBackoff(),
	}

	authz, err := delegatingAuthorizerConfig.New()
	if err != nil {
		klog.Errorf("error creating authorizer from delegating authorizer config: %v", err)
		return nil, err
	}

	return authz, nil
}

// clusterAwareAuthorizationV1Client is a thin wrapper around AuthorizationV1Interface that exposes a RESTClient()
// implementation that supports logical clusters for POST calls.
// TODO(ncdc) replace with generated clientset wrappers that are logical cluster aware.
type clusterAwareAuthorizationV1Client struct {
	authorizationv1client.AuthorizationV1Interface
	cluster logicalcluster.Name
}

// RESTClient returns a rest.Interface that supports logical clusters for POST calls.
func (c *clusterAwareAuthorizationV1Client) RESTClient() rest.Interface {
	return &clusterAwareRESTClient{
		Interface: c.AuthorizationV1Interface.RESTClient(),
		cluster:   c.cluster,
	}
}

// clusterAwareRESTClient supports logical clusters for POST calls.
type clusterAwareRESTClient struct {
	rest.Interface
	cluster logicalcluster.Name
}

// Post returns a *rest.Request for a specific logical cluster.
func (c *clusterAwareRESTClient) Post() *rest.Request {
	return c.Interface.Post().Cluster(c.cluster)
}
