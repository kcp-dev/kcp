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

	kcpkubernetesclient "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	"k8s.io/apiserver/pkg/server/options"
	"k8s.io/klog/v2"
)

type DelegatedAuthorizerFactory func(clusterName logicalcluster.Name, client kcpkubernetesclient.ClusterInterface) (authorizer.Authorizer, error)

// NewDelegatedAuthorizer returns a new authorizer for use in e.g. admission plugins that delegates
// to the kube API server via SubjectAccessReview.
func NewDelegatedAuthorizer(clusterName logicalcluster.Name, client kcpkubernetesclient.ClusterInterface) (authorizer.Authorizer, error) {
	delegatingAuthorizerConfig := &authorizerfactory.DelegatingAuthorizerConfig{
		SubjectAccessReviewClient: client.Cluster(clusterName).AuthorizationV1(),
		AllowCacheTTL:             5 * time.Minute,
		DenyCacheTTL:              30 * time.Second,
		WebhookRetryBackoff:       options.DefaultAuthWebhookRetryBackoff(),
	}

	authz, err := delegatingAuthorizerConfig.New()
	if err != nil {
		klog.Errorf("error creating authorizer from delegating authorizer config: %v", err)
		return nil, err
	}

	return authz, nil
}
