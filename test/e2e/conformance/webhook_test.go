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
	"path/filepath"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"

	webhookserver "github.com/kcp-dev/kcp/test/e2e/fixtures/webhook"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestMutatingWebhookInWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	// using known path to cert and key
	cfg := server.BaseConfig(t)

	scheme := runtime.NewScheme()
	err := admissionregistrationv1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission registration v1 scheme")
	err = v1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission v1 scheme")
	err = v1alpha1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add cowboy v1alpha1 to scheme")

	codecs := serializer.NewCodecFactory(scheme)
	deserializer := codecs.UniversalDeserializer()

	testWebhook := webhookserver.AdmissionWebhookServer{
		Response: v1.AdmissionResponse{
			Allowed: true,
		},
		ObjectGVK: schema.GroupVersionKind{
			Group:   "wildwest.dev",
			Version: "v1alpha1",
			Kind:    "Cowboy",
		},
		Deserializer: deserializer,
	}

	port, err := framework.GetFreePort(t)
	require.NoError(t, err, "failed to get free port for test webhook")
	dirPath := filepath.Dir(server.KubeconfigPath())
	testWebhook.StartTLS(t, filepath.Join(dirPath, "apiserver.crt"), filepath.Join(dirPath, "apiserver.key"), port)

	organization := framework.NewOrganizationFixture(t, server)
	logicalClusters := []logicalcluster.Path{
		framework.NewWorkspaceFixture(t, server, organization),
		framework.NewWorkspaceFixture(t, server, organization),
	}

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")
	cowbyClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct cowboy client for server")
	apiExtensionsClients, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")

	t.Logf("Install the Cowboy resources into logical clusters")
	for _, logicalCluster := range logicalClusters {
		t.Logf("Bootstrapping ClusterWorkspace CRDs in logical cluster %s", logicalCluster)
		crdClient := apiExtensionsClients.ApiextensionsV1().CustomResourceDefinitions()
		wildwest.Create(t, logicalCluster, crdClient, metav1.GroupResource{Group: "wildwest.dev", Resource: "cowboys"})
	}

	t.Logf("Installing webhook into the first workspace")
	sideEffect := admissionregistrationv1.SideEffectClassNone
	url := testWebhook.GetURL()
	webhook := &admissionregistrationv1.MutatingWebhookConfiguration{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "test-webhook"},
		Webhooks: []admissionregistrationv1.MutatingWebhook{{
			Name: "test-webhook.cowboy.io",
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL:      &url,
				CABundle: cfg.CAData,
			},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Operations: []admissionregistrationv1.OperationType{
					admissionregistrationv1.Create,
				},
				Rule: admissionregistrationv1.Rule{
					APIGroups:   []string{"wildwest.dev"},
					APIVersions: []string{"v1alpha1"},
					Resources:   []string{"cowboys"},
				},
			}},
			SideEffects:             &sideEffect,
			AdmissionReviewVersions: []string{"v1"},
		}},
	}
	_, err = kubeClusterClient.Cluster(logicalClusters[0]).AdmissionregistrationV1().MutatingWebhookConfigurations().Create(ctx, webhook, metav1.CreateOptions{})
	require.NoError(t, err, "failed to add validating webhook configurations")

	cowboy := v1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "testing",
		},
		Spec: v1alpha1.CowboySpec{},
	}

	t.Logf("Creating cowboy resource in first logical cluster")
	require.Eventually(t, func() bool {
		_, err = cowbyClusterClient.Cluster(logicalClusters[0]).WildwestV1alpha1().Cowboys("default").Create(ctx, &cowboy, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			return false
		}
		return testWebhook.Calls() >= 1

	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	// Avoid race condition here by making sure that CRD is served after installing the types into logical clusters
	t.Logf("Creating cowboy resource in second logical cluster")
	require.Eventually(t, func() bool {
		_, err = cowbyClusterClient.Cluster(logicalClusters[1]).WildwestV1alpha1().Cowboys("default").Create(ctx, &cowboy, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			return false
		}
		return true

	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	require.Equal(t, 1, testWebhook.Calls(), "expected that the webhook is not called for logical cluster where webhook is not installed")

}

func TestValidatingWebhookInWorkspace(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	// using known path to cert and key
	cfg := server.BaseConfig(t)

	scheme := runtime.NewScheme()
	err := admissionregistrationv1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission registration v1 scheme")
	err = v1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission v1 scheme")
	err = v1alpha1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add cowboy v1alpha1 to scheme")

	codecs := serializer.NewCodecFactory(scheme)
	deserializer := codecs.UniversalDeserializer()

	testWebhook := webhookserver.AdmissionWebhookServer{
		Response: v1.AdmissionResponse{
			Allowed: true,
		},
		ObjectGVK: schema.GroupVersionKind{
			Group:   "wildwest.dev",
			Version: "v1alpha1",
			Kind:    "Cowboy",
		},
		Deserializer: deserializer,
	}

	port, err := framework.GetFreePort(t)
	require.NoError(t, err, "failed to get free port for test webhook")
	dirPath := filepath.Dir(server.KubeconfigPath())
	testWebhook.StartTLS(t, filepath.Join(dirPath, "apiserver.crt"), filepath.Join(dirPath, "apiserver.key"), port)

	organization := framework.NewOrganizationFixture(t, server)
	logicalClusters := []logicalcluster.Path{
		framework.NewWorkspaceFixture(t, server, organization),
		framework.NewWorkspaceFixture(t, server, organization),
	}

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")
	cowbyClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct cowboy client for server")
	apiExtensionsClients, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")

	t.Logf("Install the Cowboy resources into logical clusters")
	for _, logicalCluster := range logicalClusters {
		t.Logf("Bootstrapping ClusterWorkspace CRDs in logical cluster %s", logicalCluster)
		crdClient := apiExtensionsClients.ApiextensionsV1().CustomResourceDefinitions()
		wildwest.Create(t, logicalCluster, crdClient, metav1.GroupResource{Group: "wildwest.dev", Resource: "cowboys"})
	}

	t.Logf("Installing webhook into the first workspace")
	sideEffect := admissionregistrationv1.SideEffectClassNone
	url := testWebhook.GetURL()
	webhook := &admissionregistrationv1.ValidatingWebhookConfiguration{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "test-webhook"},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{{
			Name: "test-webhook.cowboy.io",
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL:      &url,
				CABundle: cfg.CAData,
			},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Operations: []admissionregistrationv1.OperationType{
					admissionregistrationv1.Create,
				},
				Rule: admissionregistrationv1.Rule{
					APIGroups:   []string{"wildwest.dev"},
					APIVersions: []string{"v1alpha1"},
					Resources:   []string{"cowboys"},
				},
			}},
			SideEffects:             &sideEffect,
			AdmissionReviewVersions: []string{"v1"},
		}},
	}
	_, err = kubeClusterClient.Cluster(logicalClusters[0]).AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, webhook, metav1.CreateOptions{})
	require.NoError(t, err, "failed to add validating webhook configurations")

	cowboy := v1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "testing",
		},
		Spec: v1alpha1.CowboySpec{},
	}

	t.Logf("Creating cowboy resource in first logical cluster")
	require.Eventually(t, func() bool {
		_, err = cowbyClusterClient.Cluster(logicalClusters[0]).WildwestV1alpha1().Cowboys("default").Create(ctx, &cowboy, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			return false
		}
		return testWebhook.Calls() == 1
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	// Avoid race condition here by making sure that CRD is served after installing the types into logical clusters
	t.Logf("Creating cowboy resource in second logical cluster")
	_, err = cowbyClusterClient.Cluster(logicalClusters[1]).WildwestV1alpha1().Cowboys("default").Create(ctx, &cowboy, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create cowboy resource in second logical cluster")
	require.Equal(t, 1, testWebhook.Calls(), "expected that the webhook is not called for logical cluster where webhook is not installed")
}
