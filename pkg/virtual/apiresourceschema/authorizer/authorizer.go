/*
Copyright 2026 The kcp Authors.

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

	"k8s.io/apiserver/pkg/authorization/authorizer"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"

	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
)

// apiResourceSchemaAuthorizer checks if the user has permission to access
// APIResourceSchemas in a consumer workspace. Access is granted if the user
// has permission to read APIBindings in the consumer workspace.
//
// This is designed for provider operators who need to know what schemas
// consumers are using via their APIBindings.
type apiResourceSchemaAuthorizer struct {
	newDelegatedAuthorizer func(clusterName logicalcluster.Name) (authorizer.Authorizer, error)
}

// NewAPIResourceSchemaAuthorizer creates a new authorizer that checks
// if the user has read access to APIBindings in the consumer workspace.
// The consumer workspace is determined from the APIDomainKey in the context.
//
// Access is allowed if:
// - The user has "get", "list", or "watch" permission on "apibindings" in the consumer workspace
// - The request verb is read-only (get, list, watch).
func NewAPIResourceSchemaAuthorizer(kubeClusterClient kcpkubernetesclientset.ClusterInterface) authorizer.Authorizer {
	return &apiResourceSchemaAuthorizer{
		newDelegatedAuthorizer: func(clusterName logicalcluster.Name) (authorizer.Authorizer, error) {
			return delegated.NewDelegatedAuthorizer(clusterName, kubeClusterClient, delegated.Options{})
		},
	}
}

func (a *apiResourceSchemaAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	// Only allow read-only verbs
	switch attr.GetVerb() {
	case "get", "list", "watch":
		// Continue to authorization check
	default:
		return authorizer.DecisionDeny, "only read-only access is allowed to apiresourceschemas", nil
	}

	// Get the consumer cluster from the API domain key
	apiDomainKey := dynamiccontext.APIDomainKeyFrom(ctx)
	if apiDomainKey == "" {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("no API domain key in context")
	}

	consumerCluster := logicalcluster.Name(apiDomainKey)
	if consumerCluster.Empty() {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("invalid consumer cluster name")
	}

	// Create a delegated authorizer for the consumer cluster
	authz, err := a.newDelegatedAuthorizer(consumerCluster)
	if err != nil {
		return authorizer.DecisionNoOpinion, "",
			fmt.Errorf("error creating delegated authorizer for consumer cluster %q: %w", consumerCluster, err)
	}

	// Check if the user has permission to read APIBindings in the consumer workspace
	// This is the "invitation" - if you can read bindings, you can see what schemas they reference
	SARAttributes := authorizer.AttributesRecord{
		APIGroup:        apisv1alpha2.SchemeGroupVersion.Group,
		APIVersion:      apisv1alpha2.SchemeGroupVersion.Version,
		User:            attr.GetUser(),
		Verb:            "get", // Check for get permission as a baseline
		Resource:        "apibindings",
		ResourceRequest: true,
	}

	dec, reason, err := authz.Authorize(ctx, SARAttributes)
	if err != nil {
		return authorizer.DecisionNoOpinion, "",
			fmt.Errorf("error authorizing RBAC in consumer cluster %q: %w", consumerCluster, err)
	}

	if dec == authorizer.DecisionAllow {
		return authorizer.DecisionAllow, "", nil
	}

	return dec, fmt.Sprintf("consumer cluster: %q RBAC decision: %v - user needs permission to read apibindings",
		consumerCluster, reason), nil
}
