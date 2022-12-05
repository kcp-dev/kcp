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

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiserver/pkg/authorization/authorizer"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

type apiExportsContentAuthorizer struct {
	newDelegatedAuthorizer func(clusterName string) (authorizer.Authorizer, error)
	delegate               authorizer.Authorizer
}

// NewAPIExportsContentAuthorizer creates a new authorizer that checks
// if the user has access to the `apiexports/content` subresource using the same verb as the requested resource.
// The given kube cluster client is used to execute a SAR request against the cluster of the current in-flight API export.
// If the SAR decision allows access, the given delegate authorizer is executed to proceed the authorizer chain,
// else access is denied.
func NewAPIExportsContentAuthorizer(delegate authorizer.Authorizer, kubeClusterClient kcpkubernetesclientset.ClusterInterface) authorizer.Authorizer {
	return &apiExportsContentAuthorizer{
		newDelegatedAuthorizer: func(clusterName string) (authorizer.Authorizer, error) {
			return delegated.NewDelegatedAuthorizer(logicalcluster.New(clusterName), kubeClusterClient)
		},
		delegate: delegate,
	}
}

func (a *apiExportsContentAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	apiDomainKey := dynamiccontext.APIDomainKeyFrom(ctx)
	parts := strings.Split(string(apiDomainKey), "/")
	if len(parts) < 2 {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("invalid API domain key")
	}

	apiExportCluster, apiExportName := parts[0], parts[1]
	authz, err := a.newDelegatedAuthorizer(apiExportCluster)
	if err != nil {
		return authorizer.DecisionNoOpinion, "",
			fmt.Errorf("error creating delegated authorizer for API export %q, workspace %q: %w", apiExportName, apiExportCluster, err)
	}

	SARAttributes := authorizer.AttributesRecord{
		APIGroup:        apisv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      apisv1alpha1.SchemeGroupVersion.Version,
		User:            attr.GetUser(),
		Verb:            attr.GetVerb(),
		Name:            apiExportName,
		Resource:        "apiexports",
		ResourceRequest: true,
		Subresource:     "content",
	}

	dec, reason, err := authz.Authorize(ctx, SARAttributes)
	if err != nil {
		return authorizer.DecisionNoOpinion, "",
			fmt.Errorf("error authorizing RBAC in API export %q, workspace %q: %w", apiExportName, apiExportCluster, err)
	}

	if dec == authorizer.DecisionAllow {
		return a.delegate.Authorize(ctx, attr)
	}

	return dec, fmt.Sprintf("API export: %q, workspace: %q RBAC decision: %v",
		apiExportName, apiExportCluster, reason), nil
}
