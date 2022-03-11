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

package authorizer

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

func TestLegacyServiceAccounts(t *testing.T) {
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

	kubeClient := kubeClusterClient.Cluster(clusterName)

	t.Log("Creating namespace")
	namespace, err := kubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-sa-",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create namespace")

	t.Log("Waiting for service account to be created")
	require.Eventually(t, func() bool {
		_, err := kubeClient.CoreV1().ServiceAccounts(namespace.Name).Get(ctx, "default", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false
		} else if err != nil {
			t.Fatalf("unexpected error retrieving service account: %v", err)
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "\"default\" service account not created in namespace %s",
		helper.QualifiedObjectName(namespace),
	)

	t.Log("Waiting for service account secret to be created")
	var tokenSecret corev1.Secret
	require.Eventually(t, func() bool {
		secrets, err := kubeClient.CoreV1().Secrets(namespace.Name).List(ctx, metav1.ListOptions{})
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

	saRestConfig, err := server.DefaultConfig()
	saRestConfig.BearerToken = string(tokenSecret.Data["token"])
	saKubeClusterClient, err := kubernetes.NewClusterForConfig(saRestConfig)
	require.NoError(t, err)

	t.Run("Accessing workspace with the service account", func(t *testing.T) {
		_, err = saKubeClusterClient.Cluster(clusterName).Discovery().ServerGroups()
		require.NoError(t, err)
	})

	t.Run("Access another workspace in the same org", func(t *testing.T) {
		t.Log("Create namespace with the same name ")
		clusterName := framework.NewWorkspaceFixture(t, server, orgClusterName, "Universal")
		kubeClient := kubeClusterClient.Cluster(clusterName)
		_, err = kubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace.Name,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err, "failed to create namespace in other workspace")

		t.Log("Accessing workspace with the service account")
		_, err = saKubeClusterClient.Cluster(clusterName).Discovery().ServerGroups()
		require.Error(t, err)
	})

	t.Run("Access an equally named workspace in another org", func(t *testing.T) {
		t.Log("Create namespace with the same name")
		orgClusterName := framework.NewOrganizationFixture(t, server)
		clusterName := framework.NewWorkspaceFixture(t, server, orgClusterName, "Universal")
		kubeClient := kubeClusterClient.Cluster(clusterName)
		_, err = kubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace.Name,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err, "failed to create namespace in other workspace")

		t.Log("Accessing workspace with the service account")
		_, err = saKubeClusterClient.Cluster(clusterName).Discovery().ServerGroups()
		require.Error(t, err)
	})
}
