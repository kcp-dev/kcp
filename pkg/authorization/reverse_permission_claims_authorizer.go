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

package authorization

import (
	"context"
	"fmt"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	apisv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
)

func NewReversePermissionClaimsAuthorizer(apiBindingsLister apisv1alpha1listers.APIBindingClusterLister, delegate authorizer.Authorizer) authorizer.Authorizer {
	return &reversePermissionClaimsAuthorizer{
		getAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
			return apiBindingsLister.Cluster(clusterName).List(labels.Everything())
		},
		delegate: delegate,
	}
}

type reversePermissionClaimsAuthorizer struct {
	getAPIBindings func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
	delegate       authorizer.Authorizer
}

func (a *reversePermissionClaimsAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("error getting cluster from request: %w", err)
	}

	allBindings, err := a.getAPIBindings(clusterName)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("error getting API binding reference: %w", err)
	}

	var apiBindings []*apisv1alpha1.APIBinding
	for _, apiBinding := range allBindings {
		for _, claim := range apiBinding.Spec.PermissionClaims {
			if claim.State != apisv1alpha1.ClaimAccepted {
				continue
			}
			if claim.Group == attr.GetAPIGroup() && claim.Resource == attr.GetResource() {
				apiBindings = append(apiBindings, apiBinding)
				break
			}
		}
	}

	if len(apiBindings) == 0 {
		return DelegateAuthorization("no API binding bound", a.delegate).Authorize(ctx, attr)
	}

	authorizers := newRestrictToVerbsAuthorizers(attr, apiBindings)

	// In case there are no matching permission claims, we have to allow access,
	// as the service provider did not claim the requested resource
	// or the user did not accept any permission claim.
	if len(authorizers) == 0 {
		return DelegateAuthorization("unclaimed resource", a.delegate).Authorize(ctx, attr)
	}

	dec, reason, err := IntersectionAuthorizer(authorizers).Authorize(ctx, attr)
	if err != nil {
		return dec, reason, fmt.Errorf("error authorizing permission claims: %w", err)
	}
	if dec != authorizer.DecisionAllow {
		return authorizer.DecisionNoOpinion, reason, nil
	}
	return DelegateAuthorization(reason, a.delegate).Authorize(ctx, attr)
}

func newRestrictToVerbsAuthorizers(attr authorizer.Attributes, bindings []*apisv1alpha1.APIBinding) []authorizer.Authorizer {
	var authorizers []authorizer.Authorizer
	for _, binding := range bindings {
		for _, claim := range binding.Spec.PermissionClaims {
			claim := claim
			if claim.Group != attr.GetAPIGroup() || claim.Resource != attr.GetResource() {
				continue
			}
			// claim is not accepted by the user that is importing it, we can skip it.
			if claim.State != apisv1alpha1.ClaimAccepted {
				continue
			}
			authorizers = append(authorizers, NewRestrictToVerbsAuthorizer(binding, &claim.PermissionClaim))
		}
	}
	return authorizers
}

func NewRestrictToVerbsAuthorizer(binding *apisv1alpha1.APIBinding, claim *apisv1alpha1.PermissionClaim) authorizer.Authorizer {
	return authorizer.AuthorizerFunc(func(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
		requestingAllResources := attr.GetName() == "" && attr.GetNamespace() == ""
		resourceSelectorMatches := claim.All || requestingAllResources
		for _, claimedResource := range claim.ResourceSelector {
			var ns string
			if claimedResource.Namespace != "" {
				ns = attr.GetNamespace()
			}
			var name string
			if claimedResource.Name != "" {
				name = attr.GetName()
			}
			if claimedResource.Name == name && claimedResource.Namespace == ns {
				resourceSelectorMatches = true
				break
			}
		}

		if !resourceSelectorMatches {
			return authorizer.DecisionAllow, fmt.Sprintf("resource doesn't match any resource selector in API binding name=%q", binding.GetName()), nil
		}

		if len(claim.Verbs.RestrictTo) == 1 && claim.Verbs.RestrictTo[0] == "*" {
			return authorizer.DecisionAllow, fmt.Sprintf("permission claim restricts to \"*\" in API binding name=%q", binding.GetName()), nil
		}

		for _, verb := range claim.Verbs.RestrictTo {
			if attr.GetVerb() == verb {
				return authorizer.DecisionAllow, fmt.Sprintf("verb is restricted to in permission claim in API binding name=%q", binding.GetName()), nil
			}
		}

		return authorizer.DecisionDeny, fmt.Sprintf("verb is not restricted to in API binding name=%q", binding.GetName()), nil
	})
}
