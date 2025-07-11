/*
Copyright 2025 The KCP Authors.

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

package authorizer

import (
	"context"
	"fmt"
	"slices"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	kcpkubeclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/replication/apidomainkey"
)

type wrappedResourceAuthorizer struct {
	newDelegatedAuthorizer func(clusterName logicalcluster.Name) (authorizer.Authorizer, error)
}

var readOnlyVerbs = []string{"get", "list", "watch"}

func NewWrappedResourceAuthorizer(kubeClusterClient kcpkubeclientset.ClusterInterface) authorizer.Authorizer {
	return &wrappedResourceAuthorizer{
		newDelegatedAuthorizer: func(clusterName logicalcluster.Name) (authorizer.Authorizer, error) {
			return delegated.NewDelegatedAuthorizer(clusterName, kubeClusterClient, delegated.Options{})
		},
	}
}

func (a *wrappedResourceAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	targetCluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("error getting valid cluster from context: %w", err)
	}

	parsedKey, err := apidomainkey.Parse(dynamiccontext.APIDomainKeyFrom(ctx))
	if err != nil {
		return authorizer.DecisionNoOpinion, "",
			fmt.Errorf("invalid API domain key")
	}

	if !slices.Contains(readOnlyVerbs, attr.GetVerb()) {
		return authorizer.DecisionDeny, "write access to CachedResource is not allowed from virtual workspace", nil
	}

	authz, err := a.newDelegatedAuthorizer(targetCluster.Name)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", err
	}

	dec, reason, err := authz.Authorize(ctx, attr)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("error authorizing RBAC in workspace %q for CachedResource %s|%s: %w",
			targetCluster.Name, parsedKey.CachedResourceCluster.String(), parsedKey.CachedResourceName, err)
	}

	if dec == authorizer.DecisionAllow {
		return authorizer.DecisionAllow, fmt.Sprintf("CachedResource: %s|%s, workspace: %q RBAC decision: %v",
			parsedKey.CachedResourceCluster.String(), parsedKey.CachedResourceName, targetCluster.Name, reason), nil
	}

	return authorizer.DecisionDeny, fmt.Sprintf("CachedResource: %s|%s, workspace: %q RBAC decision: %v",
		parsedKey.CachedResourceCluster.String(), parsedKey.CachedResourceName, targetCluster.Name, reason), nil
}
