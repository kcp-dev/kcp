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
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestRequeueWhenIdentitySecretAdded(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	workspaceClusterName := framework.NewWorkspaceFixture(t, server, orgClusterName)
	t.Logf("Running test in cluster %s", workspaceClusterName)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := clientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	kubeClusterClient, err := kubernetes.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	apiExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-export",
		},
		Spec: apisv1alpha1.APIExportSpec{
			Identity: &apisv1alpha1.Identity{
				SecretRef: &corev1.SecretReference{
					Namespace: "default",
					Name:      "identity1",
				},
			},
		},
	}

	t.Logf("Creating APIExport with reference to nonexistent identity secret")
	apiExportClient := kcpClusterClient.ApisV1alpha1().APIExports()

	_, err = apiExportClient.Create(logicalcluster.WithCluster(ctx, workspaceClusterName), apiExport, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExport")

	t.Logf("Verifying the APIExport gets IdentityVerificationFailedReason")
	require.Eventually(t, func() bool {
		export, err := apiExportClient.Get(logicalcluster.WithCluster(ctx, workspaceClusterName), apiExport.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}

		if c := conditions.Get(export, apisv1alpha1.APIExportIdentityValid); c != nil {
			return c.Reason == apisv1alpha1.IdentityVerificationFailedReason
		}

		return false
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected ")

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "identity1",
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"key": "abcd1234",
		},
	}

	t.Logf("Creating the referenced secret")
	_, err = kubeClusterClient.CoreV1().Secrets("default").Create(logicalcluster.WithCluster(ctx, workspaceClusterName), secret, metav1.CreateOptions{})
	require.NoError(t, err, "error creating secret")

	t.Logf("Verifying the APIExport verifies and the identity and gets the expected generated identity hash")
	var gotHash string
	require.Eventually(t, func() bool {
		export, err := apiExportClient.Get(logicalcluster.WithCluster(ctx, workspaceClusterName), apiExport.Name, metav1.GetOptions{})
		if err != nil {
			return false
		}

		if !conditions.IsTrue(export, apisv1alpha1.APIExportIdentityValid) {
			return false
		}

		gotHash = export.Status.IdentityHash
		return gotHash == "e9cee71ab932fde863338d08be4de9dfe39ea049bdafb342ce659ec5450b69ae"
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected identity valid condition and matching hash, got hash %s", gotHash)
}
