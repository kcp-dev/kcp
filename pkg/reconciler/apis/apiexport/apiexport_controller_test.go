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

package apiexport

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

func TestReconcile(t *testing.T) {
	tests := map[string]struct {
		secretRefSet                         bool
		secretExists                         bool
		createSecretError                    error
		keyMissing                           bool
		secretHashDoesntMatchAPIExportStatus bool
		apiExportHasExpectedHash             bool
		apiExportHasSomeOtherHash            bool
		hasPreexistingVerifyFailure          bool
		listClusterWorkspaceShardsError      error

		apiBindings []interface{}

		wantGenerationFailed          bool
		wantError                     bool
		wantCreateSecretCalled        bool
		wantUnsetIdentity             bool
		wantDefaultSecretRef          bool
		wantStatusHashSet             bool
		wantVerifyFailure             bool
		wantIdentityValid             bool
		wantVirtualWorkspaceURLsError bool
		wantVirtualWorkspaceURLsReady bool
	}{
		"create secret when ref is nil and secret doesn't exist": {
			secretExists: false,

			wantCreateSecretCalled: true,
			wantDefaultSecretRef:   true,
		},
		"error creating secret - identity not valid": {
			secretExists:      false,
			createSecretError: errors.New("foo"),

			wantCreateSecretCalled: true,
			wantUnsetIdentity:      true,
			wantGenerationFailed:   true,
			wantError:              true,
		},
		"set ref if default secret exists": {
			secretExists: true,

			wantDefaultSecretRef: true,
		},
		"status hash updated when unset": {
			secretRefSet: true,
			secretExists: true,

			wantStatusHashSet: true,
			wantIdentityValid: true,
		},
		"identity verification fails when reference secret doesn't exist": {
			secretRefSet: true,
			secretExists: false,

			wantVerifyFailure: true,
		},
		"identity verification fails when hash from secret's key differs with APIExport's hash": {
			secretRefSet:                         true,
			secretExists:                         true,
			apiExportHasExpectedHash:             true,
			secretHashDoesntMatchAPIExportStatus: true,

			wantVerifyFailure: true,
		},
		"able to fix identity verification by returning to secret with correct key/hash": {
			secretRefSet:                true,
			secretExists:                true,
			apiExportHasExpectedHash:    true,
			hasPreexistingVerifyFailure: true,

			wantIdentityValid: true,
		},
		"error listing clusterworkspaceshards": {
			secretRefSet: true,
			secretExists: true,

			wantStatusHashSet: true,
			wantIdentityValid: true,

			apiBindings: []interface{}{
				"something",
			},
			listClusterWorkspaceShardsError: errors.New("foo"),
			wantVirtualWorkspaceURLsError:   true,
		},
		"virtualWorkspaceURLs set when APIBindings present": {
			secretRefSet: true,
			secretExists: true,

			wantStatusHashSet: true,
			wantIdentityValid: true,

			apiBindings: []interface{}{
				"something",
			},
			wantVirtualWorkspaceURLsReady: true,
		},
	}

	for name, tc := range tests {
		tc := tc // to avoid t.Parallel() races

		t.Run(name, func(t *testing.T) {
			createSecretCalled := false

			expectedKey := "abc"
			expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte(expectedKey)))
			someOtherKey := "def"

			c := &controller{
				getNamespace: func(clusterName logicalcluster.Name, name string) (*corev1.Namespace, error) {
					return &corev1.Namespace{}, nil
				},
				createNamespace: func(ctx context.Context, clusterName logicalcluster.Path, ns *corev1.Namespace) error {
					return nil
				},
				secretNamespace: "default-ns",
				getSecret: func(ctx context.Context, clusterName logicalcluster.Name, ns, name string) (*corev1.Secret, error) {
					if tc.secretExists {
						secret := &corev1.Secret{
							Data: map[string][]byte{},
						}
						if !tc.keyMissing {
							if tc.secretHashDoesntMatchAPIExportStatus {
								secret.Data[apisv1alpha1.SecretKeyAPIExportIdentity] = []byte(someOtherKey)
							} else {
								secret.Data[apisv1alpha1.SecretKeyAPIExportIdentity] = []byte(expectedKey)
							}
						}
						return secret, nil
					}

					return nil, apierrors.NewNotFound(corev1.Resource("secrets"), name)
				},
				createSecret: func(ctx context.Context, clusterName logicalcluster.Path, secret *corev1.Secret) error {
					createSecretCalled = true
					return tc.createSecretError
				},
				getAPIBindingsForAPIExport: func(_ logicalcluster.Name, _ string) ([]interface{}, error) {
					if len(tc.apiBindings) > 0 {
						return tc.apiBindings, nil
					}

					return make([]interface{}, 0), nil
				},
				listClusterWorkspaceShards: func() ([]*tenancyv1alpha1.ClusterWorkspaceShard, error) {
					if tc.listClusterWorkspaceShardsError != nil {
						return nil, tc.listClusterWorkspaceShardsError
					}

					return []*tenancyv1alpha1.ClusterWorkspaceShard{
						{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Name: "shard1",
							},
							Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
								ExternalURL: "https://server-1.kcp.dev/",
							},
						},
						{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Name: "shard2",
							},
							Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
								ExternalURL: "https://server-2.kcp.dev/",
							},
						},
					}, nil
				},
			}

			apiExport := &apisv1alpha1.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "root:org:ws",
					},
					Name: "my-export",
				},
			}

			if tc.secretRefSet {
				apiExport.Spec.Identity = &apisv1alpha1.Identity{
					SecretRef: &corev1.SecretReference{
						Namespace: "somens",
						Name:      "somename",
					},
				}
			}

			if tc.apiExportHasSomeOtherHash {
				apiExport.Status.IdentityHash = "asdfasdfasdfasdf"
			}
			if tc.apiExportHasExpectedHash {
				apiExport.Status.IdentityHash = expectedHash
			}

			if tc.hasPreexistingVerifyFailure {
				conditions.MarkFalse(apiExport, apisv1alpha1.APIExportIdentityValid, apisv1alpha1.IdentityVerificationFailedReason, conditionsv1alpha1.ConditionSeverityError, "")
			}

			err := c.reconcile(context.Background(), apiExport)
			if tc.wantError {
				require.Error(t, err, "expected an error")
			} else {
				require.NoError(t, err, "expected no error")
			}

			require.Equal(t, tc.wantCreateSecretCalled, createSecretCalled, "expected to try to create secret")

			if !tc.wantUnsetIdentity {
				if tc.wantDefaultSecretRef {
					require.Equal(t, "default-ns", apiExport.Spec.Identity.SecretRef.Namespace)
					require.Equal(t, apiExport.Name, apiExport.Spec.Identity.SecretRef.Name)
				} else {
					require.Equal(t, "somens", apiExport.Spec.Identity.SecretRef.Namespace)
					require.Equal(t, "somename", apiExport.Spec.Identity.SecretRef.Name)
				}
			}

			if tc.wantStatusHashSet {
				hashBytes := sha256.Sum256([]byte("abc"))
				hash := fmt.Sprintf("%x", hashBytes)
				require.Equal(t, hash, apiExport.Status.IdentityHash)
			}

			if tc.wantGenerationFailed {
				requireConditionMatches(t, apiExport,
					conditions.FalseCondition(
						apisv1alpha1.APIExportIdentityValid,
						apisv1alpha1.IdentityGenerationFailedReason,
						conditionsv1alpha1.ConditionSeverityError,
						"",
					),
				)
			}

			if tc.wantVerifyFailure {
				requireConditionMatches(t, apiExport,
					conditions.FalseCondition(
						apisv1alpha1.APIExportIdentityValid,
						apisv1alpha1.IdentityVerificationFailedReason,
						conditionsv1alpha1.ConditionSeverityError,
						"",
					),
				)
			}

			if tc.wantIdentityValid {
				requireConditionMatches(t, apiExport, conditions.TrueCondition(apisv1alpha1.APIExportIdentityValid))
			}

			if tc.wantVirtualWorkspaceURLsError {
				requireConditionMatches(t, apiExport,
					conditions.FalseCondition(
						apisv1alpha1.APIExportVirtualWorkspaceURLsReady,
						apisv1alpha1.ErrorGeneratingURLsReason,
						conditionsv1alpha1.ConditionSeverityError,
						"",
					),
				)
			}

			if tc.wantVirtualWorkspaceURLsReady {
				requireConditionMatches(t, apiExport, conditions.TrueCondition(apisv1alpha1.APIExportVirtualWorkspaceURLsReady))
			}
		})
	}
}

// requireConditionMatches looks for a condition matching c in g. Only fields that are set in c are compared (Type is
// required, though). If c.Message is set, the test performed is contains rather than an exact match.
func requireConditionMatches(t *testing.T, g conditions.Getter, c *conditionsv1alpha1.Condition) {
	actual := conditions.Get(g, c.Type)

	require.NotNil(t, actual, "missing condition %q", c.Type)

	if c.Status != "" {
		require.Equal(t, c.Status, actual.Status)
	}

	if c.Severity != "" {
		require.Equal(t, c.Severity, actual.Severity)
	}

	if c.Reason != "" {
		require.Equal(t, c.Reason, actual.Reason)
	}

	if c.Message != "" {
		require.Contains(t, actual.Message, c.Message)
	}
}
