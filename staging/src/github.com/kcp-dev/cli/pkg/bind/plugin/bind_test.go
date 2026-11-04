/*
Copyright 2023 The kcp Authors.

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

package plugin

import (
	"testing"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

func TestBindOptionsValidate(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		description string
		bindOptions BindOptions
		wantValid   bool
	}{
		{
			description: "Fully-qualified APIExport reference with dashes",
			bindOptions: BindOptions{APIExportRef: "test-root:test-workspace:test-apiexport-123"},
			wantValid:   true,
		},
		{
			description: "Fully-qualified APIExport reference with dots in the name",
			bindOptions: BindOptions{APIExportRef: "test-root:test-workspace:test.apiexport.123"},
			wantValid:   true,
		},
		{
			description: "Fully-qualified APIExport reference with special characters in the name",
			bindOptions: BindOptions{APIExportRef: "test-root:test-workspace:test@apiexport=123"},
			wantValid:   true,
		},
		{
			description: "Fully-qualified APIExport reference with upper-case characters in the name",
			bindOptions: BindOptions{APIExportRef: "test-root:test-workspace:testAPIExport123"},
			wantValid:   true,
		},
		{
			description: "Fully-qualified APIExport reference with special characters in the path",
			bindOptions: BindOptions{APIExportRef: "test-root:t@st-workspace:test-apiexport"},
			wantValid:   false,
		},
		{
			description: "Fully-qualified APIExport reference with upper-case characters in the path",
			bindOptions: BindOptions{APIExportRef: "test-root:TestWorkspace:test-apiexport"},
			wantValid:   false,
		},
		{
			description: "Valid accepted permission claims",
			bindOptions: BindOptions{
				APIExportRef:             "test-root:test-workspace:test-apiexport",
				AcceptedPermissionClaims: []string{"group.resource"},
			},
			wantValid: true,
		},
		{
			description: "Valid rejected permission claims",
			bindOptions: BindOptions{
				APIExportRef:             "test-root:test-workspace:test-apiexport",
				RejectedPermissionClaims: []string{"resource.group"},
			},
			wantValid: true,
		},
		{
			description: "Conflicting accepted and rejected permission claims",
			bindOptions: BindOptions{
				APIExportRef:             "test-root:test-workspace:test-apiexport",
				AcceptedPermissionClaims: []string{"resource.group"},
				RejectedPermissionClaims: []string{"resource.group"},
			},
			wantValid: false,
		},
		{
			description: "Invalid accepted permission claim format",
			bindOptions: BindOptions{
				APIExportRef:             "test-root:test-workspace:test-apiexport",
				AcceptedPermissionClaims: []string{"invalidclaim"},
			},
			wantValid: false,
		},
		{
			description: "Invalid rejected permission claim format",
			bindOptions: BindOptions{
				APIExportRef:             "test-root:test-workspace:test-apiexport",
				RejectedPermissionClaims: []string{"invalidclaim"},
			},
			wantValid: false,
		},
	}

	for _, c := range testCases {
		t.Run(c.description, func(t *testing.T) {
			t.Parallel()
			err := c.bindOptions.Validate()

			if (err != nil) && c.wantValid {
				t.Errorf("wanted %+v to be valid, but it's invalid", c.bindOptions)
			}

			if (err == nil) && !c.wantValid {
				t.Errorf("wanted %+v to be invalid, but it's valid", c.bindOptions)
			}
		})
	}
}

func TestParsePermissionClaim(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		description string
		claim       string
		accepted    bool
		wantError   bool
	}{
		{
			description: "Valid accepted permission claim",
			claim:       "resource.group",
			accepted:    true,
			wantError:   false,
		},
		{
			description: "Valid rejected permission claim",
			claim:       "resource.group",
			accepted:    false,
			wantError:   false,
		},
		{
			description: "Invalid permission claim format",
			claim:       "invalidclaim",
			accepted:    true,
			wantError:   true,
		},
		{
			description: "Empty permission claim",
			claim:       "",
			accepted:    true,
			wantError:   true,
		},
		{
			description: "Core group permission claim",
			claim:       "resource.core",
			accepted:    true,
			wantError:   false,
		},
	}

	for _, c := range testCases {
		t.Run(c.description, func(t *testing.T) {
			t.Parallel()
			b := &BindOptions{}
			err := b.parsePermissionClaim(c.claim, c.accepted)

			if (err != nil) && !c.wantError {
				t.Errorf("wanted %q to be parsed without error, but got error: %v", c.claim, err)
			}

			if (err == nil) && c.wantError {
				t.Errorf("wanted %q to be parsed with error, but got no error", c.claim)
			}
		})
	}
}

// TestBindOptionsAcceptRejectAll verifies that when accept-all or reject-all flags
// are set, the corresponding AcceptablePermissionClaim slices are populated correctly.
func TestBindOptionsAcceptRejectAll(t *testing.T) {
	t.Parallel()
	// Accept-all permission claims
	t.Run("accept-all permission claims", func(t *testing.T) {
		t.Parallel()
		b := &BindOptions{}

		// Simulate APIExport permissions claims
		apiClaims := []struct {
			Group    string
			Resource string
		}{
			{"group1", "res1"},
			{"group2", "res2"},
		}

		for _, claim := range apiClaims {
			parsedClaim := apisv1alpha2.AcceptablePermissionClaim{
				ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
					PermissionClaim: apisv1alpha2.PermissionClaim{
						GroupResource: apisv1alpha2.GroupResource{
							Group:    claim.Group,
							Resource: claim.Resource,
						},
						Verbs: []string{"*"},
					},
					Selector: apisv1alpha2.PermissionClaimSelector{MatchAll: true},
				},
				State: apisv1alpha2.ClaimAccepted,
			}
			b.acceptedPermissionClaims = append(b.acceptedPermissionClaims, parsedClaim)
		}

		if len(b.acceptedPermissionClaims) != len(apiClaims) {
			t.Errorf("expected %d accepted claims, got %d", len(apiClaims), len(b.acceptedPermissionClaims))
		}

		for _, c := range b.acceptedPermissionClaims {
			if c.State != apisv1alpha2.ClaimAccepted {
				t.Errorf("expected state Accepted, got %v", c.State)
			}
		}
	})

	// Reject-all permission claims
	t.Run("reject-all permission claims", func(t *testing.T) {
		t.Parallel()
		b := &BindOptions{}

		apiClaims := []struct {
			Group    string
			Resource string
		}{
			{"group1", "res1"},
			{"group2", "res2"},
		}

		for _, claim := range apiClaims {
			parsedClaim := apisv1alpha2.AcceptablePermissionClaim{
				ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
					PermissionClaim: apisv1alpha2.PermissionClaim{
						GroupResource: apisv1alpha2.GroupResource{
							Group:    claim.Group,
							Resource: claim.Resource,
						},
						Verbs: []string{"*"},
					},
					Selector: apisv1alpha2.PermissionClaimSelector{MatchAll: true},
				},
				State: apisv1alpha2.ClaimRejected,
			}
			b.rejectedPermissionClaims = append(b.rejectedPermissionClaims, parsedClaim)
		}

		if len(b.rejectedPermissionClaims) != len(apiClaims) {
			t.Errorf("expected %d rejected claims, got %d", len(apiClaims), len(b.rejectedPermissionClaims))
		}

		for _, c := range b.rejectedPermissionClaims {
			if c.State != apisv1alpha2.ClaimRejected {
				t.Errorf("expected state Rejected, got %v", c.State)
			}
		}
	})
}

// TestAddAllPermissionClaims verifies that accept-all and reject-all copy the
// group, resource and identity hash of every APIExport permission claim.
func TestAddAllPermissionClaims(t *testing.T) {
	t.Parallel()

	exportClaims := []apisv1alpha1.PermissionClaim{
		{GroupResource: apisv1alpha1.GroupResource{Group: "", Resource: "configmaps"}},
		{GroupResource: apisv1alpha1.GroupResource{Group: "example.kcp.io", Resource: "widgets"}, IdentityHash: "abc123"},
	}

	testCases := []struct {
		description string
		acceptAll   bool
		wantState   apisv1alpha2.AcceptablePermissionClaimState
	}{
		{description: "accept-all", acceptAll: true, wantState: apisv1alpha2.ClaimAccepted},
		{description: "reject-all", acceptAll: false, wantState: apisv1alpha2.ClaimRejected},
	}

	for _, c := range testCases {
		t.Run(c.description, func(t *testing.T) {
			t.Parallel()
			b := &BindOptions{
				AcceptAllPermissionClaims: c.acceptAll,
				RejectAllPermissionClaims: !c.acceptAll,
			}
			b.addAllPermissionClaims(exportClaims)

			got := b.rejectedPermissionClaims
			other := b.acceptedPermissionClaims
			if c.acceptAll {
				got, other = other, got
			}
			if len(other) != 0 {
				t.Errorf("expected no claims in the opposite list, got %d", len(other))
			}
			if len(got) != len(exportClaims) {
				t.Fatalf("expected %d claims, got %d", len(exportClaims), len(got))
			}
			for i, claim := range got {
				want := exportClaims[i]
				if claim.Group != want.Group || claim.Resource != want.Resource || claim.IdentityHash != want.IdentityHash {
					t.Errorf("claim %d: expected %s.%s with identity hash %q, got %s.%s with identity hash %q",
						i, want.Resource, want.Group, want.IdentityHash, claim.Resource, claim.Group, claim.IdentityHash)
				}
				if claim.State != c.wantState {
					t.Errorf("claim %d: expected state %s, got %s", i, c.wantState, claim.State)
				}
			}
		})
	}
}
