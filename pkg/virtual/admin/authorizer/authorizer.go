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
	"slices"

	"k8s.io/apiserver/pkg/authorization/authorizer"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

// adminAuthorizer authorizes access to the Admin workspace, keeping the
// serving path free of any dependency on the root shard. What can actually be
// written is further restricted by the storage (e.g. only allow-listed Shard
// annotations).
type adminAuthorizer struct{}

// NewAdminAuthorizer creates an authorizer for the Admin workspace. Access is
// allowed to the external administrator group system:kcp:admin and to the
// internal system:masters group.
func NewAdminAuthorizer() authorizer.Authorizer {
	return &adminAuthorizer{}
}

func (a *adminAuthorizer) Authorize(_ context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	switch attr.GetVerb() {
	case "get", "list", "watch":
	case "update", "patch":
		// only Shard objects accept (allow-listed) writes; every other view
		// is derived state and read-only.
		if attr.GetAPIGroup() != corev1alpha1.SchemeGroupVersion.Group || attr.GetResource() != "shards" {
			return authorizer.DecisionDeny, "resource is read-only in the admin workspace", nil
		}
	default:
		return authorizer.DecisionDeny, "verb not supported by the admin workspace", nil
	}

	groups := attr.GetUser().GetGroups()
	for _, g := range []string{bootstrap.SystemKcpAdminGroup, bootstrap.SystemMastersGroup} {
		if slices.Contains(groups, g) {
			return authorizer.DecisionAllow, "user is a member of the " + g + " group", nil
		}
	}

	return authorizer.DecisionDeny, "access to the admin workspace requires membership in the " + bootstrap.SystemKcpAdminGroup + " or " + bootstrap.SystemMastersGroup + " group", nil
}
