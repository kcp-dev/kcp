/*
Copyright 2025 The kcp Authors.

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

package authfixtures

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
)

func CreateServiceAccount(t *testing.T, client kcpkubernetesclientset.ClusterInterface, workspace logicalcluster.Path, namespace string, generateName string) (*corev1.ServiceAccount, *corev1.Secret) {
	t.Helper()

	t.Log("Create a service account")
	sa, err := client.Cluster(workspace).CoreV1().ServiceAccounts(namespace).Create(
		t.Context(),
		&corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: generateName,
			},
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	t.Log("Creating the service account secret")
	saSecret, err := client.Cluster(workspace).CoreV1().Secrets(namespace).Create(
		t.Context(),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: sa.Name + "-token",
				Annotations: map[string]string{
					corev1.ServiceAccountNameKey: sa.Name,
				},
			},
			Type: corev1.SecretTypeServiceAccountToken,
		},
		metav1.CreateOptions{},
	)
	require.NoError(t, err, "failed to create service account secret")

	t.Log("Waiting for service account secret to be filled")
	var tokenSecret *corev1.Secret
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		var err error
		tokenSecret, err = client.Cluster(workspace).CoreV1().Secrets(namespace).Get(t.Context(), saSecret.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("Error getting service account secret: %v", err)
		}
		return len(tokenSecret.Data) > 0, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	return sa, tokenSecret
}
