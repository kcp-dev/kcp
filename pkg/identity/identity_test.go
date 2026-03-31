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

package identity

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

// TestGenerateIdentitySecret verifies that GenerateIdentitySecret creates a Secret
// with the expected metadata and a non-empty identity key.
func TestGenerateIdentitySecret(t *testing.T) {
	namespace := "test-namespace"
	name := "test-name"

	secret, err := GenerateIdentitySecret(context.Background(), namespace, name)

	require.NoError(t, err)

	// Verify metadata
	require.Equal(t, namespace, secret.Namespace)
	require.Equal(t, name, secret.Name)

	// Verify identity key is present and non-empty
	key, exists := secret.StringData[apisv1alpha1.SecretKeyAPIExportIdentity]
	require.True(t, exists, "identity key should exist in StringData")
	require.NotEmpty(t, key, "identity key should not be empty")
}

// TestIdentityHash verifies that IdentityHash correctly computes the SHA-256
// hash of the identity key stored in a Secret, and returns an error when the key is missing.
func TestIdentityHash(t *testing.T) {
	tests := map[string]struct {
		secret        *corev1.Secret
		expectedHash  string
		expectError   bool
		errorContains string
	}{
		"valid secret returns correct hash": {
			secret: &corev1.Secret{
				Data: map[string][]byte{
					apisv1alpha1.SecretKeyAPIExportIdentity: []byte("xxx"),
				},
			},
			expectedHash: "cd2eb0837c9b4c962c22d2ff8b5441b7b45805887f051d39bf133b583baf6860",
			expectError:  false,
		},
		"missing key returns error": {
			secret: &corev1.Secret{
				Data: map[string][]byte{},
			},
			expectError:   true,
			errorContains: "missing",
		},
		"nil data returns error": {
			secret:        &corev1.Secret{},
			expectError:   true,
			errorContains: "missing",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			hash, err := IdentityHash(tt.secret)

			if tt.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errorContains)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expectedHash, hash)
		})
	}
}
