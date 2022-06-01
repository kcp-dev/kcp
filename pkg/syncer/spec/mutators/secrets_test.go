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

package mutators

import (
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSecretsMutate(t *testing.T) {
	for _, c := range []struct {
		desc                           string
		originalSecret, expectedSecret *corev1.Secret
	}{{
		desc: "A secret that is not a ServiceAccount token, should not be mutated",
		originalSecret: &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Secret",
				APIVersion: "v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-super-secret-stuff",
				Annotations: map[string]string{
					"testing": "testing",
				},
			},
			Data: map[string][]byte{
				"foo": []byte("bar"),
			},
		},
		expectedSecret: &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Secret",
				APIVersion: "v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "my-super-secret-stuff",
				Annotations: map[string]string{
					"testing": "testing",
				},
			},
			Data: map[string][]byte{
				"foo": []byte("bar"),
			},
		},
	}, {
		desc: "A ServiceAccount secret, should be mutated",
		originalSecret: &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Secret",
				APIVersion: "v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "default-token-f8f8f8",
				Annotations: map[string]string{
					"kubernetes.io/service-account.name": "default",
					"kubernetes.io/service-account.uid":  "asdasdasdasdasdsadsadas",
				},
			},
			Type: corev1.SecretTypeServiceAccountToken,
			Data: map[string][]byte{
				"token":     []byte("token"),
				"namespace": []byte("namespace"),
			},
		},
		expectedSecret: &corev1.Secret{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Secret",
				APIVersion: "v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "default-token-f8f8f8",
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"token":     []byte("token"),
				"namespace": []byte("namespace"),
			},
		},
	}} {
		t.Run(c.desc, func(t *testing.T) {
			sm := SecretMutator{}
			unstrOriginalSecret, err := toUnstructured(c.originalSecret)
			require.NoError(t, err)
			unstrExpectedSecret, err := toUnstructured(c.expectedSecret)
			require.NoError(t, err)
			// The Secret mutator doesn't use the logical cluster.
			err = sm.Mutate(unstrOriginalSecret)
			require.NoError(t, err)
			if !apiequality.Semantic.DeepEqual(unstrOriginalSecret, unstrExpectedSecret) {
				t.Errorf("secret mutated incorrectly, got: %v expected: %v", unstrOriginalSecret.Object, unstrExpectedSecret.Object)
			}
		})
	}
}
