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
	"github.com/kcp-dev/sdk/apis/core"

	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
)

// adminAuthorizer authorizes access to the Admin workspace. The workspace is
// read-only, and access to a resource is granted to whoever may read that
// resource in the root workspace - e.g. the aggregated shards view requires
// read access to shards.core.kcp.io in root, the same bar as listing shards
// there today. This keeps permissions in one well-known place (RBAC in root)
// while the serving path no longer depends on the root shard, and extends
// automatically to future Admin workspace resources.
type adminAuthorizer struct {
	newDelegatedAuthorizer func(clusterName logicalcluster.Name) (authorizer.Authorizer, error)
}

// NewAdminAuthorizer creates an authorizer for the Admin workspace. Access
// is allowed if the request verb is read-only and the user holds that verb
// on the requested resource in the root workspace.
func NewAdminAuthorizer(kubeClusterClient kcpkubernetesclientset.ClusterInterface) authorizer.Authorizer {
	return &adminAuthorizer{
		newDelegatedAuthorizer: func(clusterName logicalcluster.Name) (authorizer.Authorizer, error) {
			return delegated.NewDelegatedAuthorizer(clusterName, kubeClusterClient, delegated.Options{})
		},
	}
}

func (a *adminAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	switch attr.GetVerb() {
	case "get", "list", "watch":
	default:
		return authorizer.DecisionDeny, "the admin workspace is read-only", nil
	}

	authz, err := a.newDelegatedAuthorizer(core.RootCluster)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("error creating delegated authorizer for the root workspace: %w", err)
	}

	sarAttributes := authorizer.AttributesRecord{
		User:            attr.GetUser(),
		Verb:            attr.GetVerb(),
		APIGroup:        attr.GetAPIGroup(),
		APIVersion:      attr.GetAPIVersion(),
		Resource:        attr.GetResource(),
		Subresource:     attr.GetSubresource(),
		Name:            attr.GetName(),
		ResourceRequest: attr.IsResourceRequest(),
	}

	return authz.Authorize(ctx, sarAttributes)
}
