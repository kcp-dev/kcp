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
	"crypto/sha256"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/crypto"
)

// GenerateIdentitySecret creates a Kubernetes Secret containing a randomly generated
// identity key used for APIExport identity.
//
// The secret will be created in the given namespace with the provided name.
// The identity key is stored in StringData under apisv1alpha1.SecretKeyAPIExportIdentity.
//
// A warning is logged if key generation takes longer than 100ms.
func GenerateIdentitySecret(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	logger := klog.FromContext(ctx)

	start := time.Now()
	key := crypto.Random256BitsString()
	if dur := time.Since(start); dur > time.Millisecond*100 {
		logger.Info("identity key generation took a long time", "duration", dur)
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        name,
			Annotations: map[string]string{},
		},
		StringData: map[string]string{
			apisv1alpha1.SecretKeyAPIExportIdentity: key,
		},
	}, nil
}

// IdentityHash computes a SHA-256 hash of the identity key stored in the given Secret.
//
// The identity key is expected to be present in secret.Data under
// apisv1alpha1.SecretKeyAPIExportIdentity.
func IdentityHash(secret *corev1.Secret) (string, error) {
	key := secret.Data[apisv1alpha1.SecretKeyAPIExportIdentity]
	if len(key) == 0 {
		return "", fmt.Errorf("secret is missing data.%s", apisv1alpha1.SecretKeyAPIExportIdentity)
	}

	hashBytes := sha256.Sum256(key)
	hash := fmt.Sprintf("%x", hashBytes)
	return hash, nil
}
