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

package proxy

import (
	"context"
	"slices"

	"k8s.io/apiserver/pkg/authorization/authorizer"

	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

// metricsReaderGroups may read the front-proxy's /metrics. The proxy has
// no RBAC of its own and must not depend on the root shard, so access is
// decided from the caller's groups: the administrator groups that the Admin
// workspace accepts, and system:kcp:metrics-reader, the name of the
// ClusterRole that authorizes scraping on the shards, used as a group here.
var metricsReaderGroups = []string{
	bootstrappolicy.SystemKcpAdminGroup,
	bootstrappolicy.SystemMastersGroup,
	bootstrappolicy.SystemKcpMetricsReader,
}

type metricsGroupAuthorizer struct{}

func newMetricsGroupAuthorizer() authorizer.Authorizer {
	return metricsGroupAuthorizer{}
}

// Authorize allows members of metricsReaderGroups and has no opinion on
// everybody else, so that a further authorizer in a union can still allow.
func (metricsGroupAuthorizer) Authorize(_ context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	if attr.GetUser() == nil {
		return authorizer.DecisionNoOpinion, "", nil
	}
	for _, group := range attr.GetUser().GetGroups() {
		if slices.Contains(metricsReaderGroups, group) {
			return authorizer.DecisionAllow, "user is a member of the " + group + " group", nil
		}
	}
	return authorizer.DecisionNoOpinion, "", nil
}
