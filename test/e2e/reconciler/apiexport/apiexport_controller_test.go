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
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestRequeueWhenIdentitySecretAdded(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	workspacePath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	t.Logf("Running test in cluster %s", workspacePath)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	apiExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-export",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Identity: &apisv1alpha2.Identity{
				SecretRef: &corev1.SecretReference{
					Namespace: "default",
					Name:      "identity1",
				},
			},
		},
	}

	t.Logf("Creating APIExport with reference to nonexistent identity secret")
	apiExportClient := kcpClusterClient.ApisV1alpha2().APIExports()

	_, err = apiExportClient.Cluster(workspacePath).Create(t.Context(), apiExport, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExport")

	t.Logf("Verifying the APIExport gets IdentityVerificationFailedReason")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return apiExportClient.Cluster(workspacePath).Get(t.Context(), apiExport.Name, metav1.GetOptions{})
	}, kcptestinghelpers.IsNot(apisv1alpha2.APIExportIdentityValid).WithReason(apisv1alpha2.IdentityVerificationFailedReason))

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
	_, err = kubeClusterClient.Cluster(workspacePath).CoreV1().Secrets("default").Create(t.Context(), secret, metav1.CreateOptions{})
	require.NoError(t, err, "error creating secret")

	t.Logf("Verifying the APIExport verifies and the identity and gets the expected generated identity hash")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return apiExportClient.Cluster(workspacePath).Get(t.Context(), apiExport.Name, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))

	export, err := apiExportClient.Cluster(workspacePath).Get(t.Context(), apiExport.Name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "e9cee71ab932fde863338d08be4de9dfe39ea049bdafb342ce659ec5450b69ae", export.Status.IdentityHash)
}
