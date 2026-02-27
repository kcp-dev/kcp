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

package initialization

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

func TestGenerateAPIBindingName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		exportName           string
		expectedPrefixLength int
	}{
		"short export name": {
			exportName:           "a",
			expectedPrefixLength: 1,
		},
		"max length without truncation": {
			exportName:           strings.Repeat("a", 247),
			expectedPrefixLength: 247,
		},
		"over max length": {
			exportName:           strings.Repeat("a", 248),
			expectedPrefixLength: 247,
		},
	}

	re := regexp.MustCompile(`^(a+)-(.+)$`)

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			clusterName := logicalcluster.Name("root:some:ws")
			exportPath := "root:some:export:ws"

			generated := generateAPIBindingName(clusterName, exportPath, tc.exportName)
			t.Logf("generated: %s", generated)

			matches := re.FindStringSubmatch(generated)
			require.Len(t, matches, 3)
			require.Len(t, matches[1], tc.expectedPrefixLength)
			require.Len(t, matches[2], 5)
		})
	}
}

func TestGenerateAPIBindingNameWithMultipleSimilarLongNames(t *testing.T) {
	t.Parallel()

	clusterName := logicalcluster.Name("root:some:ws")
	exportPath := "root:some:export:ws"

	// 252 chars
	longName1 := "thisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongname"
	// 263 chars
	longName2 := "thisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethisisareallylongnamethatdiffers"
	generated1 := generateAPIBindingName(clusterName, exportPath, longName1)
	t.Logf("generated1: %s", generated1)
	generated2 := generateAPIBindingName(clusterName, exportPath, longName2)
	t.Logf("generated2: %s", generated2)
	require.Len(t, generated1, 253)
	require.Len(t, generated2, 253)
	require.NotEqual(t, generated1, generated2, "expected different generated names")
}

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
		expectedError     bool
	}{
		"returns matchLabels selector when workspace has matching APIBinding with label selector": {
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
		"returns MatchAll selector when workspace only has MatchAll selector": {
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
		"returns nil when no APIBinding matches the export reference": {
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
		"returns error when listAPIBindings fails for workspace": {
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
			expectedError:     true,
		},
	}

	for testName, tc := range tests {
		t.Run(testName, func(t *testing.T) {
			b := &APIBinder{
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

			result, err := b.findSelectorInWorkspace(ctx, tc.workspacePath, tc.exportRef, tc.exportClaim)
			if tc.expectedError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

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
