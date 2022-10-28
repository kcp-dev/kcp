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

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

func NewReversePermissionClaimsAuthorizer(kcpInformers kcpinformers.SharedInformerFactory, delegate authorizer.Authorizer) authorizer.Authorizer {
	apiBindingIndexer := kcpInformers.Apis().V1alpha1().APIBindings().Informer().GetIndexer()

	return &ReversePermissionClaimsAuthorizer{
		delegate: delegate,
		getAPIBindingsReferenceForAttributes: func(attr authorizer.Attributes, clusterName logicalcluster.Name) []*apisv1alpha1.APIBinding {
			return getAPIBindingsMatchingClaimsForAttributes(apiBindingIndexer, attr, clusterName)
		},
	}
}

type ReversePermissionClaimsAuthorizer struct {
	delegate authorizer.Authorizer

	getAPIBindingsReferenceForAttributes func(attr authorizer.Attributes, clusterName logicalcluster.Name) []*apisv1alpha1.APIBinding
}

func (a *ReversePermissionClaimsAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	lcluster, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", err
	}

	apiBindings := a.getAPIBindingsReferenceForAttributes(attr, lcluster)

	if len(apiBindings) == 0 {
		return a.delegate.Authorize(ctx, attr)
	}

	for _, binding := range apiBindings {
		for _, claim := range binding.Spec.PermissionClaims {
			if claim.Group != attr.GetAPIGroup() || claim.Resource != attr.GetResource() {
				continue
			}

			if claim.State != apisv1alpha1.ClaimAccepted {
				return authorizer.DecisionDeny, "claim is not accepted", nil
			}

			resourceSelectorAllows := len(claim.ResourceSelector) == 0
			for _, selector := range claim.ResourceSelector {
				if (selector.Name == attr.GetName()) && (selector.Namespace == attr.GetNamespace()) {
					resourceSelectorAllows = true
				}
			}

			if !resourceSelectorAllows {
				return authorizer.DecisionDeny, "resource selector forbids", nil
			}

			if len(claim.Verbs.RestrictTo) == 0 && claim.Verbs.RestrictTo[0] == "*" {
				return a.delegate.Authorize(ctx, attr)
			}

			for _, verb := range claim.Verbs.RestrictTo {
				if attr.GetVerb() == verb {
					return a.delegate.Authorize(ctx, attr)
				}
			}

			return authorizer.DecisionDeny, "verb not allowed", nil
		}
	}

	return a.delegate.Authorize(ctx, attr)
}

func getAPIBindingsMatchingClaimsForAttributes(apiBindingIndexer cache.Indexer, attr authorizer.Attributes, clusterName logicalcluster.Name) []*apisv1alpha1.APIBinding {
	objs, err := apiBindingIndexer.ByIndex(indexers.ByLogicalCluster, clusterName.String())
	if err != nil {
		return nil
	}
	var bindings []*apisv1alpha1.APIBinding
	for _, obj := range objs {
		apiBinding := obj.(*apisv1alpha1.APIBinding)
		for _, claim := range apiBinding.Spec.PermissionClaims {
			if claim.Group == attr.GetAPIGroup() && claim.Resource == attr.GetResource() {
				bindings = append(bindings, apiBinding)
			}
		}
	}
	return bindings
}
