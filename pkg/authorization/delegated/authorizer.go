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

type DelegatedAuthorizerFactory func(clusterName logicalcluster.Name, client kcpkubernetesclient.ClusterInterface, opts Options) (authorizer.Authorizer, error)

// Options provides options to customize the
// created DelegatedAuthorizer.
type Options struct {
	// AllowCacheTTL is the length of time that a successful authorization response will be cached
	AllowCacheTTL time.Duration

	// DenyCacheTTL is the length of time that an unsuccessful authorization response will be cached.
	// You generally want more responsive, "deny, try again" flows.
	DenyCacheTTL time.Duration
}

func (d *Options) defaults() {
	if d.AllowCacheTTL == 0 {
		d.AllowCacheTTL = 5 * time.Minute
	}
	if d.DenyCacheTTL == 0 {
		d.AllowCacheTTL = 30 * time.Second
	}
}

// NewDelegatedAuthorizer returns a new authorizer for use in e.g. admission plugins that delegates
// to the kube API server via SubjectAccessReview.
func NewDelegatedAuthorizer(clusterName logicalcluster.Name, client kcpkubernetesclient.ClusterInterface, opts Options) (authorizer.Authorizer, error) {
	opts.defaults()

	delegatingAuthorizerConfig := &authorizerfactory.DelegatingAuthorizerConfig{
		SubjectAccessReviewClient: client.Cluster(clusterName.Path()).AuthorizationV1(),
		AllowCacheTTL:             opts.AllowCacheTTL,
		DenyCacheTTL:              opts.DenyCacheTTL,
		WebhookRetryBackoff:       options.DefaultAuthWebhookRetryBackoff(),
	}

	authz, err := delegatingAuthorizerConfig.New()
	if err != nil {
		klog.Background().Error(err, "error creating authorizer from delegating authorizer config")
		return nil, err
	}

	return authz, nil
}
