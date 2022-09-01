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

	"github.com/kcp-dev/logicalcluster/v2"

	kaudit "k8s.io/apiserver/pkg/audit"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/client-go/kubernetes"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpauth "github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

const (
	VirtualAPIExportNotPermittedReason = "virtual apiexport access not permitted"
	VirtualAPIExportPermittedReason    = "virtual apiexport access permitted"
)

const (
	VirtualAPIExportContentAuditPrefix   = "virtual.apiexport.content.authorization.kcp.dev/"
	VirtualAPIExportContentAuditDecision = VirtualAPIExportContentAuditPrefix + "decision"
	VirtualAPIExportContentAuditReason   = VirtualAPIExportContentAuditPrefix + "reason"
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
func NewAPIExportsContentAuthorizer(delegate authorizer.Authorizer, kubeClusterClient kubernetes.ClusterInterface) authorizer.Authorizer {
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
		return authorizer.DecisionNoOpinion, VirtualAPIExportNotPermittedReason, fmt.Errorf("invalid API domain key")
	}

	apiExportCluster, apiExportName := parts[0], parts[1]
	authz, err := a.newDelegatedAuthorizer(apiExportCluster)
	if err != nil {
		return authorizer.DecisionNoOpinion, VirtualAPIExportNotPermittedReason, fmt.Errorf("error creating delegated authorizer for API export cluster: %w", err)
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
		return authorizer.DecisionNoOpinion, VirtualAPIExportNotPermittedReason, fmt.Errorf("error authorizing RBAC in API export cluster: %w", err)
	}

	kaudit.AddAuditAnnotations(
		ctx,
		VirtualAPIExportContentAuditDecision, kcpauth.DecisionString(dec),
		VirtualAPIExportContentAuditReason, fmt.Sprintf("API export cluster RBAC reason: %v", reason),
	)

	if dec == authorizer.DecisionAllow {
		return a.delegate.Authorize(ctx, attr)
	}

	return authorizer.DecisionNoOpinion, VirtualAPIExportNotPermittedReason, nil
}
