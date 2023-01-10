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

package authorizer

import (
	"context"
	"fmt"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

func NewPermissionClaimsAuthorizer(kcpInformers kcpinformers.SharedInformerFactory, delegate authorizer.Authorizer) authorizer.Authorizer {
	apiBindingLister := kcpInformers.Apis().V1alpha1().APIBindings().Lister()
	apiExportLister := kcpInformers.Apis().V1alpha1().APIExports().Lister()

	return &permissionClaimsAuthorizer{
		getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha1.APIExport, error) {
			return apiExportLister.Cluster(logicalcluster.Name(clusterName)).Get(apiExportName)
		},
		listAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
			return apiBindingLister.Cluster(clusterName).List(labels.Everything())
		},
		listAllAPIBindings: func() ([]*apisv1alpha1.APIBinding, error) {
			return apiBindingLister.List(labels.Everything())
		},
		delegate: delegate,
	}
}

type permissionClaimsAuthorizer struct {
	getAPIExport       func(clusterName, apiExportName string) (*apisv1alpha1.APIExport, error)
	listAllAPIBindings func() ([]*apisv1alpha1.APIBinding, error)
	listAPIBindings    func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
	delegate           authorizer.Authorizer
}

func (a *permissionClaimsAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	if !attr.IsResourceRequest() {
		return authorization.DelegateAuthorization("non-resource request", a.delegate).Authorize(ctx, attr)
	}

	clusterName, isWildcardRequest, err := genericapirequest.ClusterNameOrWildcardFrom(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("error getting cluster from request: %w", err)
	}

	apiDomainKey := dynamiccontext.APIDomainKeyFrom(ctx)
	parts := strings.Split(string(apiDomainKey), "/")
	if len(parts) < 2 {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("invalid API domain key")
	}

	claimingAPIExportCluster := parts[0]
	claimingAPIExportName := parts[1]

	apiExport, err := a.getAPIExport(claimingAPIExportCluster, claimingAPIExportName)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("error finding API export %q/%q: %w", claimingAPIExportCluster, claimingAPIExportName, err)
	}

	// if the requested resource is a bound resource it cannot be claimed.
	if isBoundResource(attr, apiExport) {
		return authorization.DelegateAuthorization("bound resource", a.delegate).Authorize(ctx, attr)
	}

	// Find binding that matches the requested API export in the requested workspace.
	// If the requested API export has no corresponding API binding then the access must be denied
	// as the user did not accept any permission claims.
	allBindings, err := func() ([]*apisv1alpha1.APIBinding, error) {
		if isWildcardRequest {
			return a.listAllAPIBindings()
		}
		return a.listAPIBindings(clusterName)
	}()
	if err != nil {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("error finding API bindings for API export %q/%q: %w", claimingAPIExportCluster, claimingAPIExportName, err)
	}
	for _, binding := range allBindings {
		if binding.Spec.Reference.Export == nil {
			continue
		}

		if binding.Spec.Reference.Export.Name != apiExport.Name ||
			binding.Spec.Reference.Export.Path != logicalcluster.From(apiExport).String() {
			continue
		}

		for _, claim := range binding.Spec.PermissionClaims {
			if claim.Group != attr.GetAPIGroup() || claim.Resource != attr.GetResource() {
				continue
			}
			// If at least one claimed resource is not accepted by the user, access must be denied
			if claim.State != apisv1alpha1.ClaimAccepted {
				return authorizer.DecisionNoOpinion, fmt.Sprintf("claim %v of API export %q/%q is not accepted", claim, logicalcluster.From(apiExport), apiExport.Name), nil
			}
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
				continue
			}

			return authorization.DelegateAuthorization(fmt.Sprintf("claimed resource in API binding name=%q", binding.GetName()), a.delegate).Authorize(ctx, attr)
		}
	}

	return authorizer.DecisionNoOpinion, "unclaimed resource", nil
}

func isBoundResource(attr authorizer.Attributes, apiExport *apisv1alpha1.APIExport) bool {
	for _, schema := range apiExport.Spec.LatestResourceSchemas {
		_, resource, group, ok := split3(schema, ".")
		if !ok {
			continue
		}
		if group == attr.GetAPIGroup() && resource == attr.GetResource() {
			return true
		}
	}
	return false
}

func split3(s string, sep string) (string, string, string, bool) {
	comps := strings.SplitN(s, sep, 3)
	if len(comps) != 3 {
		return "", "", "", false
	}
	return comps[0], comps[1], comps[2], true
}
