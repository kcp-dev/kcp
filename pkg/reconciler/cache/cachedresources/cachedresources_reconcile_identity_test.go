/*
Copyright 2025 The KCP Authors.

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

package cachedresources

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

func TestReconcileIdentity(t *testing.T) {
	tests := map[string]struct {
		CachedResource            *cachev1alpha1.CachedResource
		reconciler                *identity
		expectedErr               error
		expectedStatus            reconcileStatus
		expectedConditions        conditionsv1alpha1.Conditions
		expectedIdentitySecretRef *cachev1alpha1.Identity
		expectedIdentityHash      string
	}{
		"identity in spec is missing and create fails": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "cluster_name",
					},
				},
			},
			reconciler: &identity{
				ensureSecretNamespaceExists: func(ctx context.Context, clusterName logicalcluster.Name, defaultSecretNamespace string) {},
				getSecret: func(ctx context.Context, clusterName logicalcluster.Name, namespace, name string) (*corev1.Secret, error) {
					return nil, apierrors.NewNotFound(corev1.Resource("resources"), name)
				},
				createIdentitySecret: func(ctx context.Context, clusterName logicalcluster.Path, defaultSecretNamespace, name string) error {
					return fmt.Errorf("create failed")
				},
				secretNamespace: "secret-ns",
			},
			expectedErr:    fmt.Errorf("create failed"),
			expectedStatus: reconcileStatusStop,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceIdentityValid,
					cachev1alpha1.IdentityGenerationFailedReason,
					conditionsv1alpha1.ConditionSeverityError,
					"Error creating identity secret: create failed",
				),
			},
		},
		"identity in spec is missing and create succeeds": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "cluster_name",
					},
					Name: "cr-name",
				},
			},
			reconciler: &identity{
				ensureSecretNamespaceExists: func(ctx context.Context, clusterName logicalcluster.Name, defaultSecretNamespace string) {},
				getSecret: func(ctx context.Context, clusterName logicalcluster.Name, namespace, name string) (*corev1.Secret, error) {
					return nil, apierrors.NewNotFound(corev1.Resource("resources"), name)
				},
				createIdentitySecret: func(ctx context.Context, clusterName logicalcluster.Path, defaultSecretNamespace, name string) error {
					return nil
				},
				secretNamespace: "cr-identity-ns",
			},
			expectedErr:        nil,
			expectedStatus:     reconcileStatusStopAndRequeue,
			expectedConditions: nil,
			expectedIdentitySecretRef: &cachev1alpha1.Identity{
				SecretRef: &corev1.SecretReference{
					Name:      "cr-name",
					Namespace: "cr-identity-ns",
				},
			},
		},
		"identity in spec is present and key in secret is missing": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "cluster_name",
					},
					Name: "cr-name",
				},
				Spec: cachev1alpha1.CachedResourceSpec{
					Identity: &cachev1alpha1.Identity{
						SecretRef: &corev1.SecretReference{
							Name:      "cr-name",
							Namespace: "cr-identity-ns",
						},
					},
				},
			},
			reconciler: &identity{
				getSecret: func(ctx context.Context, clusterName logicalcluster.Name, namespace, name string) (*corev1.Secret, error) {
					return &corev1.Secret{}, nil
				},
			},
			expectedErr:    fmt.Errorf("secret is missing data.key"),
			expectedStatus: reconcileStatusStop,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceIdentityValid,
					cachev1alpha1.IdentityVerificationFailedReason,
					conditionsv1alpha1.ConditionSeverityError,
					"secret is missing data.key",
				),
			},
			expectedIdentitySecretRef: &cachev1alpha1.Identity{
				SecretRef: &corev1.SecretReference{
					Name:      "cr-name",
					Namespace: "cr-identity-ns",
				},
			},
		},
		"identity in spec is present and identity hash in status is not matching": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "cluster_name",
					},
					Name: "cr-name",
				},
				Spec: cachev1alpha1.CachedResourceSpec{
					Identity: &cachev1alpha1.Identity{
						SecretRef: &corev1.SecretReference{
							Name:      "cr-name",
							Namespace: "cr-identity-ns",
						},
					},
				},
				Status: cachev1alpha1.CachedResourceStatus{
					IdentityHash: "some-sha256-digest-for-xxx",
				},
			},
			reconciler: &identity{
				getSecret: func(ctx context.Context, clusterName logicalcluster.Name, namespace, name string) (*corev1.Secret, error) {
					return &corev1.Secret{
						Data: map[string][]byte{
							"key": []byte("xxx"),
						},
					}, nil
				},
			},
			expectedErr:    fmt.Errorf(`hash mismatch: identity secret hash "cd2eb0837c9b4c962c22d2ff8b5441b7b45805887f051d39bf133b583baf6860" must match status.identityHash "some-sha256-digest-for-xxx"`),
			expectedStatus: reconcileStatusStop,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceIdentityValid,
					cachev1alpha1.IdentityVerificationFailedReason,
					conditionsv1alpha1.ConditionSeverityError,
					`hash mismatch: identity secret hash "cd2eb0837c9b4c962c22d2ff8b5441b7b45805887f051d39bf133b583baf6860" must match status.identityHash "some-sha256-digest-for-xxx"`,
				),
			},
			expectedIdentitySecretRef: &cachev1alpha1.Identity{
				SecretRef: &corev1.SecretReference{
					Name:      "cr-name",
					Namespace: "cr-identity-ns",
				},
			},
			expectedIdentityHash: "some-sha256-digest-for-xxx",
		},
		"identity in spec is present and identity hash in status is missing": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "cluster_name",
					},
					Name: "cr-name",
				},
				Spec: cachev1alpha1.CachedResourceSpec{
					Identity: &cachev1alpha1.Identity{
						SecretRef: &corev1.SecretReference{
							Name:      "cr-name",
							Namespace: "cr-identity-ns",
						},
					},
				},
			},
			reconciler: &identity{
				getSecret: func(ctx context.Context, clusterName logicalcluster.Name, namespace, name string) (*corev1.Secret, error) {
					return &corev1.Secret{
						Data: map[string][]byte{
							"key": []byte("xxx"),
						},
					}, nil
				},
			},
			expectedErr:    nil,
			expectedStatus: reconcileStatusContinue,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.TrueCondition(cachev1alpha1.CachedResourceIdentityValid),
			},
			expectedIdentitySecretRef: &cachev1alpha1.Identity{
				SecretRef: &corev1.SecretReference{
					Name:      "cr-name",
					Namespace: "cr-identity-ns",
				},
			},
			expectedIdentityHash: "cd2eb0837c9b4c962c22d2ff8b5441b7b45805887f051d39bf133b583baf6860",
		},
		"identity in spec is present and identity hash in status is matching": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "cluster_name",
					},
					Name: "cr-name",
				},
				Spec: cachev1alpha1.CachedResourceSpec{
					Identity: &cachev1alpha1.Identity{
						SecretRef: &corev1.SecretReference{
							Name:      "cr-name",
							Namespace: "cr-identity-ns",
						},
					},
				},
				Status: cachev1alpha1.CachedResourceStatus{
					IdentityHash: "cd2eb0837c9b4c962c22d2ff8b5441b7b45805887f051d39bf133b583baf6860",
				},
			},
			reconciler: &identity{
				getSecret: func(ctx context.Context, clusterName logicalcluster.Name, namespace, name string) (*corev1.Secret, error) {
					return &corev1.Secret{
						Data: map[string][]byte{
							"key": []byte("xxx"),
						},
					}, nil
				},
			},
			expectedErr:    nil,
			expectedStatus: reconcileStatusContinue,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.TrueCondition(cachev1alpha1.CachedResourceIdentityValid),
			},
			expectedIdentitySecretRef: &cachev1alpha1.Identity{
				SecretRef: &corev1.SecretReference{
					Name:      "cr-name",
					Namespace: "cr-identity-ns",
				},
			},
			expectedIdentityHash: "cd2eb0837c9b4c962c22d2ff8b5441b7b45805887f051d39bf133b583baf6860",
		},
	}

	for testName, tt := range tests {
		t.Run(testName, func(t *testing.T) {
			status, err := tt.reconciler.reconcile(context.Background(), tt.CachedResource)

			resetLastTransitionTime(tt.expectedConditions)
			resetLastTransitionTime(tt.CachedResource.Status.Conditions)

			if tt.expectedErr != nil {
				require.Error(t, err)
				require.Equal(t, tt.expectedErr.Error(), err.Error())
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tt.expectedStatus, status, "reconcile status mismatch")
			require.Equal(t, tt.expectedConditions, tt.CachedResource.Status.Conditions, "conditions mismatch")
			require.Equal(t, tt.expectedIdentitySecretRef, tt.CachedResource.Spec.Identity, "Identity secret ref mismatch")
			require.Equal(t, tt.expectedIdentityHash, tt.CachedResource.Status.IdentityHash, "Identity hash mismatch")
		})
	}
}

func resetLastTransitionTime(conditions conditionsv1alpha1.Conditions) {
	// We don't care about LastTransitionTime.
	for i := range conditions {
		conditions[i].LastTransitionTime = metav1.Time{}
	}
}
