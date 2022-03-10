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

package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestServiceAccountTokenController(t *testing.T) {
	t.Parallel()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	server := framework.SharedKcpServer(t)
	orgClusterName := framework.NewOrganizationFixture(t, server)
	clusterName := framework.NewWorkspaceFixture(t, server, orgClusterName, "Universal")

	cfg, err := server.DefaultConfig()
	require.NoError(t, err)

	kubeClusterClient, err := kubernetes.NewClusterForConfig(cfg)
	require.NoError(t, err)

	orgKubeClient := kubeClusterClient.Cluster(clusterName)

	namespace, err := orgKubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-sa-",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create namespace")

	require.Eventually(t, func() bool {
		_, err := orgKubeClient.CoreV1().ServiceAccounts(namespace.Name).Get(ctx, "default", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false
		} else if err != nil {
			t.Fatalf("unexpected error retrieving service account: %v", err)
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "\"default\" service account not created in namespace %s",
		helper.QualifiedObjectName(namespace),
	)

	var tokenSecret corev1.Secret
	require.Eventually(t, func() bool {
		secrets, err := orgKubeClient.CoreV1().Secrets(namespace.Name).List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "failed to list secrets")

		for _, secret := range secrets.Items {
			if secret.Annotations[corev1.ServiceAccountNameKey] == "default" {
				tokenSecret = secret
				return true
			}
		}
		return false
	}, wait.ForeverTestTimeout, time.Millisecond*100, "token secret for default service account not created")

	t.Logf("Token secret: %v", tokenSecret)

	// Use the token to access the secret
	// Create a namespace with the same name in a different workspace
	// Verify that it is not possible to read the namespace in the other workspace
}
