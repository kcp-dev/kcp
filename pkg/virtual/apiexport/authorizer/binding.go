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
	"strings"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
)

type boundAPIAuthorizer struct {
	getAPIBindingByExport func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha1.APIBinding, error)

	delegate authorizer.Authorizer
}

var readOnlyVerbs = []string{"get", "list", "watch"}

func NewBoundAPIAuthorizer(delegate authorizer.Authorizer, apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer, kubeClusterClient kcpkubernetesclientset.ClusterInterface) authorizer.Authorizer {
	apiBindingLister := apiBindingInformer.Lister()

	return &boundAPIAuthorizer{
		delegate: delegate,
		getAPIBindingByExport: func(clusterName, apiExportName, apiExportCluster string) (*apisv1alpha1.APIBinding, error) {
			bindings, err := apiBindingLister.Cluster(logicalcluster.Name(clusterName)).List(labels.Everything())
			if err != nil {
				return nil, err
			}

			for _, binding := range bindings {
				if binding == nil {
					continue
				}

				if binding.Spec.Reference.Export != nil && binding.Spec.Reference.Export.Name == apiExportName && binding.Status.APIExportClusterName == apiExportCluster {
					return binding, nil
				}
			}

			return nil, fmt.Errorf("no suitable binding found")
		},
	}
}

func (a *boundAPIAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	targetCluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("error getting valid cluster from context: %w", err)
	}

	if targetCluster.Wildcard || attr.GetResource() == "" {
		// if the target is the wildcard cluster or it's a non-resurce URL request,
		// we can skip checking the APIBinding in the target cluster.
		return a.delegate.Authorize(ctx, attr)
	}

	apiDomainKey := dynamiccontext.APIDomainKeyFrom(ctx)
	parts := strings.Split(string(apiDomainKey), "/")
	if len(parts) < 2 {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("invalid API domain key")
	}
	apiExportCluster, apiExportName := parts[0], parts[1]

	apiBinding, err := a.getAPIBindingByExport(targetCluster.Name.String(), apiExportName, apiExportCluster)
	if err != nil {
		return authorizer.DecisionDeny, "could not find suitable APIBinding in target logical cluster", nil //nolint:nilerr // this is on purpose, we want to deny, not return a server error
	}

	// check if request is for a bound resource.
	for _, resource := range apiBinding.Status.BoundResources {
		if resource.Group == attr.GetAPIGroup() && resource.Resource == attr.GetResource() {
			return a.delegate.Authorize(ctx, attr)
		}
	}

	// check if a resource claim for this resource has been accepted.
	for _, permissionClaim := range apiBinding.Spec.PermissionClaims {
		if permissionClaim.State != apisv1alpha1.ClaimAccepted {
			// if the claim is not accepted it cannot be used.
			continue
		}

		if permissionClaim.Group == attr.GetAPIGroup() && permissionClaim.Resource == attr.GetResource() {
			return a.delegate.Authorize(ctx, attr)
		}
	}

	// special case: APIBindings are always available from an APIExport VW,
	// but the provider should only be allowed to access them read-only to avoid privilege escalation.
	if attr.GetAPIGroup() == apisv1alpha1.SchemeGroupVersion.Group && attr.GetResource() == "apibindings" {
		if !slices.Contains(readOnlyVerbs, attr.GetVerb()) {
			return authorizer.DecisionNoOpinion, "write access to APIBinding is not allowed from virtual workspace", nil
		}

		return a.delegate.Authorize(ctx, attr)
	}

	// if we cannot find the API bound to the logical cluster, we deny.
	// The APIExport owner has not been invited in.
	return authorizer.DecisionDeny, "failed to find suitable reason to allow access in APIBinding", nil
}
