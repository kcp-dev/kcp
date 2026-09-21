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
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

func TestAdminAuthorizer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		groups []string
		verb   string
		want   authorizer.Decision
	}{
		{
			// The external administrator identity, passed through by the
			// front-proxy.
			name:   "system:kcp:admin may read",
			groups: []string{bootstrap.SystemKcpAdminGroup, user.AllAuthenticated},
			verb:   "list",
			want:   authorizer.DecisionAllow,
		},
		{
			// The front-proxy discovers shards through this view with its
			// --root-kubeconfig client certificate, which carries
			// system:masters. Denying it here would break shard discovery, so
			// this case is the regression guard.
			name:   "system:masters may read",
			groups: []string{bootstrap.SystemMastersGroup},
			verb:   "list",
			want:   authorizer.DecisionAllow,
		},
		{
			name:   "system:masters may watch",
			groups: []string{bootstrap.SystemMastersGroup},
			verb:   "watch",
			want:   authorizer.DecisionAllow,
		},
		{
			name:   "both groups may read",
			groups: []string{bootstrap.SystemKcpAdminGroup, bootstrap.SystemMastersGroup},
			verb:   "get",
			want:   authorizer.DecisionAllow,
		},
		{
			// An ordinary authenticated user must not reach the shard
			// topology through this view.
			name:   "plain authenticated user is denied",
			groups: []string{user.AllAuthenticated},
			verb:   "list",
			want:   authorizer.DecisionDeny,
		},
		{
			name:   "no groups is denied",
			groups: nil,
			verb:   "list",
			want:   authorizer.DecisionDeny,
		},
		{
			// The verb gate runs before the group check: shards register
			// themselves and own their objects, so even a privileged caller
			// cannot create or delete them here.
			name:   "system:masters may not create",
			groups: []string{bootstrap.SystemMastersGroup},
			verb:   "create",
			want:   authorizer.DecisionDeny,
		},
		{
			name:   "system:kcp:admin may not delete",
			groups: []string{bootstrap.SystemKcpAdminGroup},
			verb:   "delete",
			want:   authorizer.DecisionDeny,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, reason, err := NewAdminAuthorizer().Authorize(context.Background(), authorizer.AttributesRecord{
				User:            &user.DefaultInfo{Name: "tester", Groups: tc.groups},
				Verb:            tc.verb,
				APIGroup:        "core.kcp.io",
				APIVersion:      "v1alpha1",
				Resource:        "shards",
				ResourceRequest: true,
			})
			require.NoError(t, err)
			require.Equal(t, tc.want, got, "reason: %s", reason)
		})
	}
}
