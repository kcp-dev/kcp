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

package defaultapibindinglifecycle

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

func TestFindSelectorInWorkspace(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		workspacePath     logicalcluster.Path
		exportRef         tenancyv1alpha1.APIExportReference
		exportClaim       apisv1alpha2.PermissionClaim
		workspaceBindings []*apisv1alpha2.APIBinding
		listBindingsError error
		expectedSelector  *apisv1alpha2.PermissionClaimSelector
		expectedFound     bool
	}{
		"returns matchLabels selector when parent workspace has matching APIBinding with label selector": {
			workspacePath: logicalcluster.NewPath("root:parent"),
			exportRef: tenancyv1alpha1.APIExportReference{
				Path:   "root:export-ws",
				Export: "test.export",
			},
			exportClaim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{
					Group:    "",
					Resource: "configmaps",
				},
				IdentityHash: "hash123",
				Verbs:        []string{"get", "list"},
			},
			workspaceBindings: []*apisv1alpha2.APIBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-binding",
					},
					Spec: apisv1alpha2.APIBindingSpec{
						Reference: apisv1alpha2.BindingReference{
							Export: &apisv1alpha2.ExportBindingReference{
								Path: "root:export-ws",
								Name: "test.export",
							},
						},
						PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
							{
								ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
									PermissionClaim: apisv1alpha2.PermissionClaim{
										GroupResource: apisv1alpha2.GroupResource{
											Group:    "",
											Resource: "configmaps",
										},
										IdentityHash: "hash123",
									},
									Selector: apisv1alpha2.PermissionClaimSelector{
										LabelSelector: metav1.LabelSelector{
											MatchLabels: map[string]string{
												"platform-mesh.io/enabled": "true",
											},
										},
									},
								},
								State: apisv1alpha2.ClaimAccepted,
							},
						},
					},
				},
			},
			expectedSelector: &apisv1alpha2.PermissionClaimSelector{
				LabelSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"platform-mesh.io/enabled": "true",
					},
				},
			},
			expectedFound: true,
		},
		"returns MatchAll selector when parent workspace only has MatchAll selector": {
			workspacePath: logicalcluster.NewPath("root:parent"),
			exportRef: tenancyv1alpha1.APIExportReference{
				Path:   "root:export-ws",
				Export: "test.export",
			},
			exportClaim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{
					Group:    "",
					Resource: "configmaps",
				},
				IdentityHash: "hash123",
				Verbs:        []string{"get", "list"},
			},
			workspaceBindings: []*apisv1alpha2.APIBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-binding",
					},
					Spec: apisv1alpha2.APIBindingSpec{
						Reference: apisv1alpha2.BindingReference{
							Export: &apisv1alpha2.ExportBindingReference{
								Path: "root:export-ws",
								Name: "test.export",
							},
						},
						PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
							{
								ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
									PermissionClaim: apisv1alpha2.PermissionClaim{
										GroupResource: apisv1alpha2.GroupResource{
											Group:    "",
											Resource: "configmaps",
										},
										IdentityHash: "hash123",
									},
									Selector: apisv1alpha2.PermissionClaimSelector{
										MatchAll: true,
									},
								},
								State: apisv1alpha2.ClaimAccepted,
							},
						},
					},
				},
			},
			expectedSelector: &apisv1alpha2.PermissionClaimSelector{
				MatchAll: true,
			},
			expectedFound: true,
		},
		"returns nil where no APIBinding matches the export reference": {
			workspacePath: logicalcluster.NewPath("root:parent"),
			exportRef: tenancyv1alpha1.APIExportReference{
				Path:   "root:export-ws",
				Export: "test.export",
			},
			exportClaim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{
					Group:    "",
					Resource: "configmaps",
				},
				IdentityHash: "hash123",
				Verbs:        []string{"get", "list"},
			},
			workspaceBindings: []*apisv1alpha2.APIBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "other-binding",
					},
					Spec: apisv1alpha2.APIBindingSpec{
						Reference: apisv1alpha2.BindingReference{
							Export: &apisv1alpha2.ExportBindingReference{
								Path: "root:other-ws",
								Name: "other.export",
							},
						},
					},
				},
			},
			expectedFound: false,
		},
		"returns nil when permission claim is rejected": {
			workspacePath: logicalcluster.NewPath("root:parent"),
			exportRef: tenancyv1alpha1.APIExportReference{
				Path:   "root:export-ws",
				Export: "test.export",
			},
			exportClaim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{
					Group:    "",
					Resource: "configmaps",
				},
				IdentityHash: "hash123",
				Verbs:        []string{"get", "list"},
			},
			workspaceBindings: []*apisv1alpha2.APIBinding{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-binding",
					},
					Spec: apisv1alpha2.APIBindingSpec{
						Reference: apisv1alpha2.BindingReference{
							Export: &apisv1alpha2.ExportBindingReference{
								Path: "root:export-ws",
								Name: "test.export",
							},
						},
						PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
							{
								ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
									PermissionClaim: apisv1alpha2.PermissionClaim{
										GroupResource: apisv1alpha2.GroupResource{
											Group:    "",
											Resource: "configmaps",
										},
										IdentityHash: "hash123",
									},
									Selector: apisv1alpha2.PermissionClaimSelector{
										LabelSelector: metav1.LabelSelector{
											MatchLabels: map[string]string{
												"platform-mesh.io/enabled": "true",
											},
										},
									},
								},
								State: apisv1alpha2.ClaimRejected,
							},
						},
					},
				},
			},
			expectedFound: false,
		},
		"returns nil when listAPIBindings fails for workspace": {
			workspacePath: logicalcluster.NewPath("root:parent"),
			exportRef: tenancyv1alpha1.APIExportReference{
				Path:   "root:export-ws",
				Export: "test.export",
			},
			exportClaim: apisv1alpha2.PermissionClaim{
				GroupResource: apisv1alpha2.GroupResource{
					Group:    "",
					Resource: "configmaps",
				},
				IdentityHash: "hash123",
				Verbs:        []string{"get", "list"},
			},
			listBindingsError: fmt.Errorf("workspace not found"),
			expectedFound:     false,
		},
	}

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			c := &DefaultAPIBindingController{
				listAPIBindingsByPath: func(ctx context.Context, path logicalcluster.Path) ([]*apisv1alpha2.APIBinding, error) {
					if tc.listBindingsError != nil {
						return nil, tc.listBindingsError
					}
					if path == tc.workspacePath {
						return tc.workspaceBindings, nil
					}
					return nil, nil
				},
			}

			ctx := klog.NewContext(context.Background(), klog.Background())
			logger := klog.FromContext(ctx)

			result := c.findSelectorInWorkspace(ctx, tc.workspacePath, tc.exportRef, tc.exportClaim, logger)

			if !tc.expectedFound {
				require.Nil(t, result, "expected no selector to be found")
				return
			}

			require.NotNil(t, result, "expected to find a selector")
			if tc.expectedSelector != nil {
				require.Equal(t, tc.expectedSelector.MatchAll, result.MatchAll, "MatchAll should match")
				require.Equal(t, tc.expectedSelector.LabelSelector.MatchLabels, result.LabelSelector.MatchLabels, "MatchLabels should match")
				require.Equal(t, tc.expectedSelector.LabelSelector.MatchExpressions, result.LabelSelector.MatchExpressions, "MatchExpressions should match")
			}
		})
	}
}
