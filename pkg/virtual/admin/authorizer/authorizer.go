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

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

// adminAuthorizer authorizes access to the Admin workspace. Access is
// granted only to members of the system:kcp:admin group, keeping the serving
// path free of any dependency on the root shard. What can actually be
// written is further restricted by the storage (e.g. only allow-listed Shard
// annotations).
type adminAuthorizer struct{}

// NewAdminAuthorizer creates an authorizer for the Admin workspace. Access
// is allowed only to members of the system:kcp:admin group.
func NewAdminAuthorizer() authorizer.Authorizer {
	return &adminAuthorizer{}
}

func (a *adminAuthorizer) Authorize(_ context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	switch attr.GetVerb() {
	case "get", "list", "watch", "update", "patch":
	default:
		return authorizer.DecisionDeny, "verb not supported by the admin workspace", nil
	}

	if slices.Contains(attr.GetUser().GetGroups(), bootstrap.SystemKcpAdminGroup) {
		return authorizer.DecisionAllow, "user is a member of the " + bootstrap.SystemKcpAdminGroup + " group", nil
	}

	return authorizer.DecisionDeny, "access to the admin workspace requires membership in the " + bootstrap.SystemKcpAdminGroup + " group", nil
}
