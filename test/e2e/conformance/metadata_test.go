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
	"encoding/json"
	"testing"
	"time"

	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestMetadataMutations(t *testing.T) {
	// Verify that strategic merge patch of built-in Kubernetes resources that are added to kcp as CRDs (such as
	// deployments) is not allowed to change metadata such as creation timestamp.
	// https://github.com/kcp-dev/kcp/issues/1647

	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cfg := server.BaseConfig(t)

	workspaceName := framework.NewOrganizationFixture(t, server)

	workspaceCfg := kcpclienthelper.ConfigWithCluster(cfg, workspaceName)
	workspaceCRDClient, err := apiextensionclientset.NewForConfig(workspaceCfg)
	require.NoError(t, err, "error creating crd cluster client")

	kube.Create(t, workspaceCRDClient.ApiextensionsV1().CustomResourceDefinitions(), metav1.GroupResource{Group: "apps.k8s.io", Resource: "deployments"})

	kubeClusterClient, err := kubernetes.NewForConfig(kcpclienthelper.NewClusterConfig(cfg))
	require.NoError(t, err, "error creating kube cluster client")

	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "mutation-test-",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"foo": "bar"},
			},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{},
				},
			},
		},
	}

	t.Logf("Creating deployment")
	d, err = kubeClusterClient.AppsV1().Deployments("default").Create(logicalcluster.WithCluster(ctx, workspaceName), d, metav1.CreateOptions{})
	require.NoError(t, err, "error creating deployment")

	originalCreationTimestamp := d.CreationTimestamp

	updated := d.DeepCopy()
	updated.CreationTimestamp.Time = originalCreationTimestamp.Add(-24 * time.Hour * 365)

	patch, err := strategicpatch.CreateTwoWayMergePatch(encodeJSON(t, d), encodeJSON(t, updated), &appsv1.Deployment{})
	require.NoError(t, err, "error creating patch")

	t.Logf("Patching deployment - trying to change creation timestamp")
	patched, err := kubeClusterClient.AppsV1().Deployments("default").Patch(logicalcluster.WithCluster(ctx, workspaceName), d.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	require.NoError(t, err)
	t.Logf("Verifying creation timestamp was not modified")
	require.Equal(t, originalCreationTimestamp, patched.GetCreationTimestamp())

}

func encodeJSON(t *testing.T, obj interface{}) []byte {
	ret, err := json.Marshal(obj)
	require.NoError(t, err)
	return ret
}
