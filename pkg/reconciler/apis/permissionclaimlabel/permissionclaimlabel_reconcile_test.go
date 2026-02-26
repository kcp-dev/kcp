/*
Copyright 2022 The kcp Authors.

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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

func TestClaimSetKeys(t *testing.T) {
	tests := map[string]struct {
		claim apisv1alpha2.PermissionClaim
		key   string
	}{
		"core gr": {
			claim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{
					Group:    "",
					Resource: "configmaps",
				},
				IdentityHash: "",
			},
			key: "configmaps//",
		},
		"non-core built-in gr": {
			claim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{
					Group:    "rbac.authorization.k8s.io",
					Resource: "roles",
				},
				IdentityHash: "",
			},
			key: "roles/rbac.authorization.k8s.io/",
		},
		"3rd party gr + hash": {
			claim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{
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

func TestSelectorChangeDetection(t *testing.T) {
	baseClaim := apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{
			Group:    "",
			Resource: "secrets",
		},
		IdentityHash: "test-hash",
	}
	key := setKeyForClaim(baseClaim)

	tests := map[string]struct {
		acceptedSelector apisv1alpha2.PermissionClaimSelector
		appliedSelector  apisv1alpha2.PermissionClaimSelector
		shouldDetect     bool
	}{
		"matchAll to matchLabels should be detected": {
			acceptedSelector: apisv1alpha2.PermissionClaimSelector{
				MatchAll: false,
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"foo": "bar",
					},
				},
			},
			appliedSelector: apisv1alpha2.PermissionClaimSelector{
				MatchAll: true,
			},
			shouldDetect: true,
		},
		"same selector should not be detected": {
			acceptedSelector: apisv1alpha2.PermissionClaimSelector{
				MatchAll: false,
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"foo": "bar",
					},
				},
			},
			appliedSelector: apisv1alpha2.PermissionClaimSelector{
				MatchAll: false,
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"foo": "bar",
					},
				},
			},
			shouldDetect: false,
		},
	}

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			expectedClaims := sets.New(key)
			acceptedClaims := sets.New(key)
			appliedClaims := sets.New(key)
			acceptedClaimsMap := map[string]apisv1alpha2.ScopedPermissionClaim{
				key: {
					PermissionClaim: baseClaim,
					Selector:        tc.acceptedSelector,
				},
			}
			appliedClaimsMap := map[string]apisv1alpha2.ScopedPermissionClaim{
				key: {
					PermissionClaim: baseClaim,
					Selector:        tc.appliedSelector,
				},
			}

			selectorChanges := detectSelectorChanges(
				expectedClaims, acceptedClaims, appliedClaims,
				acceptedClaimsMap, appliedClaimsMap,
				klog.Background(),
			)

			if tc.shouldDetect {
				require.True(t, selectorChanges.Has(key), "Expected selector change to be detected")
			} else {
				require.False(t, selectorChanges.Has(key), "Expected no selector change to be detected")
			}
		})
	}
}
