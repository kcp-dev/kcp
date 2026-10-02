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

package apiexport

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"

	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
)

// railgridPolicy grants an APIExport exporting infrastructure.railgrid.ai the
// right to claim ai.railgrid.ai and edges.railgrid.ai without an identity
// hash, and reserves all three groups for the railgrid-providers group.
func railgridPolicy() *adminv1alpha1.PermissionClaimPolicy {
	return &adminv1alpha1.PermissionClaimPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "railgrid"},
		Spec: adminv1alpha1.PermissionClaimPolicySpec{
			Providers: []adminv1alpha1.PermissionClaimPolicySubject{
				{Kind: adminv1alpha1.PermissionClaimPolicySubjectGroup, Name: "railgrid-providers"},
			},
			Claims: []adminv1alpha1.PermissionClaimRule{
				{
					Claimer: "infrastructure.railgrid.ai",
					Groups:  []string{"ai.railgrid.ai", "edges.railgrid.ai"},
				},
			},
		},
	}
}

func exportWith(resourceGroups []string, claimGroups []string) *apisv1alpha2.APIExport {
	ae := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "an-export"},
	}
	for _, group := range resourceGroups {
		ae.Spec.Resources = append(ae.Spec.Resources, apisv1alpha2.ResourceSchema{
			Name:   "widgets",
			Group:  group,
			Schema: "today.widgets." + group,
			Storage: apisv1alpha2.ResourceSchemaStorage{
				CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
			},
		})
	}
	for _, group := range claimGroups {
		ae.Spec.PermissionClaims = append(ae.Spec.PermissionClaims, apisv1alpha2.PermissionClaim{
			GroupResource: apisv1alpha2.GroupResource{Group: group, Resource: "things"},
			Verbs:         []string{"get"},
		})
	}
	return ae
}

// attrFor builds create attributes when old is nil, update attributes otherwise.
func attrFor(ae, old *apisv1alpha2.APIExport, u user.Info) admission.Attributes {
	op := admission.Create
	var opts runtime.Object = &metav1.CreateOptions{}
	var oldObj runtime.Object
	if old != nil {
		op = admission.Update
		opts = &metav1.UpdateOptions{}
		oldObj = helpers.ToUnstructuredOrDie(old)
	}
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(ae),
		oldObj,
		apisv1alpha2.Kind("APIExport").WithVersion("v1alpha2"),
		"",
		ae.Name,
		apisv1alpha2.Resource("apiexports").WithVersion("v1alpha2"),
		"",
		op,
		opts,
		false,
		u,
	)
}

func TestValidatePolicies(t *testing.T) {
	t.Parallel()

	notBuiltIn := func(apis.GroupResource) bool { return false }
	provider := &user.DefaultInfo{Name: "ci", Groups: []string{"railgrid-providers"}}
	stranger := &user.DefaultInfo{Name: "mallory", Groups: []string{"system:authenticated"}}

	for name, tc := range map[string]struct {
		export    *apisv1alpha2.APIExport
		old       *apisv1alpha2.APIExport
		user      user.Info
		policies  []*adminv1alpha1.PermissionClaimPolicy
		unsynced  bool
		wantError string
	}{
		"identity-less claim allowed for the policy's claimer group": {
			export:   exportWith([]string{"infrastructure.railgrid.ai"}, []string{"ai.railgrid.ai"}),
			user:     provider,
			policies: []*adminv1alpha1.PermissionClaimPolicy{railgridPolicy()},
		},
		"identity-less claim rejected for a group not listed": {
			export:    exportWith([]string{"infrastructure.railgrid.ai"}, []string{"metrics.railgrid.ai"}),
			user:      provider,
			policies:  []*adminv1alpha1.PermissionClaimPolicy{railgridPolicy()},
			wantError: `identityHash is required`,
		},
		"identity-less claim rejected for an export that is not the claimer": {
			export:    exportWith([]string{"other.example.com"}, []string{"ai.railgrid.ai"}),
			user:      provider,
			policies:  []*adminv1alpha1.PermissionClaimPolicy{railgridPolicy()},
			wantError: `identityHash is required`,
		},
		"identity-less claim rejected when no policy exists": {
			export:    exportWith([]string{"infrastructure.railgrid.ai"}, []string{"ai.railgrid.ai"}),
			user:      provider,
			wantError: `identityHash is required`,
		},
		"identity-less claim rejected while the policy informer has not synced": {
			export:    exportWith([]string{"infrastructure.railgrid.ai"}, []string{"ai.railgrid.ai"}),
			user:      provider,
			policies:  []*adminv1alpha1.PermissionClaimPolicy{railgridPolicy()},
			unsynced:  true,
			wantError: `identityHash is required`,
		},
		"exporting a reserved group is allowed for a provider": {
			export:   exportWith([]string{"ai.railgrid.ai"}, nil),
			user:     provider,
			policies: []*adminv1alpha1.PermissionClaimPolicy{railgridPolicy()},
		},
		"exporting a reserved group is forbidden for anyone else": {
			export:    exportWith([]string{"ai.railgrid.ai"}, nil),
			user:      stranger,
			policies:  []*adminv1alpha1.PermissionClaimPolicy{railgridPolicy()},
			wantError: `is reserved by PermissionClaimPolicy "railgrid"`,
		},
		"exporting an unreserved group is allowed for anyone": {
			export:   exportWith([]string{"other.example.com"}, nil),
			user:     stranger,
			policies: []*adminv1alpha1.PermissionClaimPolicy{railgridPolicy()},
		},
		"updating an export that already had the reserved group is allowed": {
			export:   exportWith([]string{"ai.railgrid.ai"}, nil),
			old:      exportWith([]string{"ai.railgrid.ai"}, nil),
			user:     stranger,
			policies: []*adminv1alpha1.PermissionClaimPolicy{railgridPolicy()},
		},
		"a second policy reserving the same group can only narrow access": {
			export: exportWith([]string{"ai.railgrid.ai"}, nil),
			user:   provider,
			policies: []*adminv1alpha1.PermissionClaimPolicy{
				railgridPolicy(),
				{
					ObjectMeta: metav1.ObjectMeta{Name: "stricter"},
					Spec: adminv1alpha1.PermissionClaimPolicySpec{
						Providers: []adminv1alpha1.PermissionClaimPolicySubject{
							{Kind: adminv1alpha1.PermissionClaimPolicySubjectUser, Name: "someone-else"},
						},
						Claims: []adminv1alpha1.PermissionClaimRule{
							{Claimer: "ai.railgrid.ai", Groups: []string{"edges.railgrid.ai"}},
						},
					},
				},
			},
			wantError: `is reserved by PermissionClaimPolicy "stricter"`,
		},
		"adding a reserved group on update is forbidden for anyone else": {
			export:    exportWith([]string{"other.example.com", "ai.railgrid.ai"}, nil),
			old:       exportWith([]string{"other.example.com"}, nil),
			user:      stranger,
			policies:  []*adminv1alpha1.PermissionClaimPolicy{railgridPolicy()},
			wantError: `is reserved by PermissionClaimPolicy "railgrid"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			plugin := NewAPIExportAdmission(notBuiltIn)
			policies := tc.policies
			unsynced := tc.unsynced
			plugin.listPolicies = func() ([]*adminv1alpha1.PermissionClaimPolicy, bool, error) {
				if unsynced {
					return nil, false, nil
				}
				return policies, true, nil
			}

			err := plugin.Validate(context.Background(), attrFor(tc.export, tc.old, tc.user), nil)
			if tc.wantError == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantError)
		})
	}
}
