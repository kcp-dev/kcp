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

package permissionclaimlabel

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

func TestClaimSetKeys(t *testing.T) {
	tests := map[string]struct {
		claim apisv1alpha1.PermissionClaim
		key   string
	}{
		"core gr": {
			claim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{
					Group:    "",
					Resource: "configmaps",
				},
				IdentityHash: "",
			},
			key: "configmaps//",
		},
		"non-core built-in gr": {
			claim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{
					Group:    "rbac.authorization.k8s.io",
					Resource: "roles",
				},
				IdentityHash: "",
			},
			key: "roles/rbac.authorization.k8s.io/",
		},
		"3rd party gr + hash": {
			claim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{
					Group:    "apis.kcp.io",
					Resource: "apibindings",
				},
				IdentityHash: "hash",
			},
			key: "apibindings/apis.kcp.io/hash",
		},
	}

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			encoded := setKeyForClaim(tc.claim)
			require.Equal(t, tc.key, encoded)

			decoded := claimFromSetKey(tc.key)
			require.Empty(t, cmp.Diff(tc.claim, decoded))
		})
	}
}
