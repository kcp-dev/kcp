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

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestRequeueWhenIdentitySecretAdded(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := framework.NewOrganizationFixture(t, server)
	workspacePath, _ := framework.NewWorkspaceFixture(t, server, orgPath)
	t.Logf("Running test in cluster %s", workspacePath)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
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

	_, err = apiExportClient.Cluster(workspacePath).Create(ctx, apiExport, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExport")

	t.Logf("Verifying the APIExport gets IdentityVerificationFailedReason")
	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		return apiExportClient.Cluster(workspacePath).Get(ctx, apiExport.Name, metav1.GetOptions{})
	}, framework.IsNot(apisv1alpha1.APIExportIdentityValid).WithReason(apisv1alpha1.IdentityVerificationFailedReason))

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
	_, err = kubeClusterClient.Cluster(workspacePath).CoreV1().Secrets("default").Create(ctx, secret, metav1.CreateOptions{})
	require.NoError(t, err, "error creating secret")

	t.Logf("Verifying the APIExport verifies and the identity and gets the expected generated identity hash")
	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		return apiExportClient.Cluster(workspacePath).Get(ctx, apiExport.Name, metav1.GetOptions{})
	}, framework.Is(apisv1alpha1.APIExportIdentityValid))

	export, err := apiExportClient.Cluster(workspacePath).Get(ctx, apiExport.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "e9cee71ab932fde863338d08be4de9dfe39ea049bdafb342ce659ec5450b69ae", export.Status.IdentityHash)
}
