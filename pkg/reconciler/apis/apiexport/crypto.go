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
	"crypto/sha256"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/crypto"
)

func GenerateIdentitySecret(ns string, apiExportName string) (*corev1.Secret, error) {
	start := time.Now()
	key := crypto.Random256BitsString()
	if dur := time.Since(start); dur > time.Millisecond*100 {
		klog.Warningf("identity key generation took long: %s", dur)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      apiExportName,
		},
		StringData: map[string]string{
			apisv1alpha1.SecretKeyAPIExportIdentity: key,
		},
	}

	return secret, nil
}

func IdentityHash(secret *corev1.Secret) (string, error) {
	key := secret.Data[apisv1alpha1.SecretKeyAPIExportIdentity]
	if len(key) == 0 {
		return "", fmt.Errorf("secret is missing data.%s", apisv1alpha1.SecretKeyAPIExportIdentity)
	}

	hashBytes := sha256.Sum256(key)
	hash := fmt.Sprintf("%x", hashBytes)
	return hash, nil
}
