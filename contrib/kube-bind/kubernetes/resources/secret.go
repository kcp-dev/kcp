/*
Copyright 2022 The Kube Bind Authors.

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

package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

func CreateSASecret(ctx context.Context, client kubernetes.Interface, ns, saName string) (*corev1.Secret, error) {
	logger := klog.FromContext(ctx)

	secret, err := client.CoreV1().Secrets(ns).Get(ctx, saName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			secret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      saName,
					Namespace: ns,
					Annotations: map[string]string{
						ServiceAccountTokenAnnotation: saName,
					},
				},
				Type: ServiceAccountTokenType,
			}

			logger.V(1).Info("Creating service account secret", "name", secret.Name)
			return client.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
		}

		return nil, err
	}

	return secret, nil
}
