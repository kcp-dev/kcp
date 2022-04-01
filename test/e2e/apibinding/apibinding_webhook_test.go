/*
Copyright 2021 The KCP Authors.

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

package apibinding

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	webhookserver "github.com/kcp-dev/kcp/test/e2e/fixtures/webhook"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	client "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIBindingMutatingWebhook(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	sourceWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName, "Universal")
	targetWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName, "Universal")

	cfg := server.DefaultConfig(t)

	kcpClients, err := clientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := dynamic.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kubernetes.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")

	t.Logf("Install a cowboys APIResourceSchema into workspace %q", sourceWorkspace)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(sourceWorkspace).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(sourceWorkspace), mapper, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
		},
	}
	_, err = kcpClients.Cluster(sourceWorkspace).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in workspace %q that points to the today-cowboys export", targetWorkspace)
	require.NoError(t, err)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					WorkspaceName: sourceWorkspace.Base(),
					ExportName:    cowboysAPIExport.Name,
				},
			},
		},
	}

	_, err = kcpClients.Cluster(targetWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	scheme := runtime.NewScheme()
	err = admissionregistrationv1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission registration v1 scheme")
	err = v1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission v1 scheme")
	err = v1alpha1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add cowboy v1alpha1 to scheme")
	cowbyClients, err := client.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to add cowboy v1alpha1 to scheme")
	codecs := serializer.NewCodecFactory(scheme)
	deserializer := codecs.UniversalDeserializer()

	//Create Test Server and Create Validating Webhook for Cowboys in source cluster.
	testWebhook := webhookserver.WebhookServer{
		Response: v1.AdmissionResponse{
			Allowed: true,
		},
		ObjectGVK: schema.GroupVersionKind{
			Group:   "wildwest.dev",
			Version: "v1alpha1",
			Kind:    "Cowboy",
		},
		T:            t,
		Lock:         sync.Mutex{},
		Deserializer: deserializer,
	}

	port, err := framework.GetFreePort(t)
	require.NoError(t, err, "failed to get free port for test webhook")
	testWebhook.StartServer(ctx, server, port)

	t.Logf("Installing webhook into the source workspace")
	sideEffect := admissionregistrationv1.SideEffectClassNone
	webhook := &admissionregistrationv1.MutatingWebhookConfiguration{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "test-webhook"},
		Webhooks: []admissionregistrationv1.MutatingWebhook{{
			Name: "test-webhook.cowboy.io",
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL:      testWebhook.GetURL(),
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
	_, err = kubeClusterClient.Cluster(sourceWorkspace).AdmissionregistrationV1().MutatingWebhookConfigurations().Create(ctx, webhook, metav1.CreateOptions{})
	require.NoError(t, err, "failed to add validating webhook configurations")

	cowboy := v1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "testing",
		},
		Spec: v1alpha1.CowboySpec{},
	}

	// Avoid race condition here by making sure that CRD is served after installing the types into logical clusters
	t.Logf("Creating cowboy resource in target logical cluster")
	require.Eventually(t, func() bool {
		_, err = cowbyClients.Cluster(targetWorkspace).WildwestV1alpha1().Cowboys("default").Create(ctx, &cowboy, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			return false
		}
		return testWebhook.Calls >= 1

	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func TestAPIBindingValidatingWebhook(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	sourceWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName, "Universal")
	targetWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName, "Universal")

	cfg := server.DefaultConfig(t)

	kcpClients, err := clientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := dynamic.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kubernetes.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")

	t.Logf("Install a cowboys APIResourceSchema into workspace %q", sourceWorkspace)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(sourceWorkspace).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(sourceWorkspace), mapper, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
		},
	}
	_, err = kcpClients.Cluster(sourceWorkspace).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in workspace %q that points to the today-cowboys export", targetWorkspace)
	require.NoError(t, err)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					WorkspaceName: sourceWorkspace.Base(),
					ExportName:    cowboysAPIExport.Name,
				},
			},
		},
	}

	_, err = kcpClients.Cluster(targetWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	scheme := runtime.NewScheme()
	err = admissionregistrationv1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission registration v1 scheme")
	err = v1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission v1 scheme")
	err = v1alpha1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add cowboy v1alpha1 to scheme")
	cowbyClients, err := client.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to add cowboy v1alpha1 to scheme")
	codecs := serializer.NewCodecFactory(scheme)
	deserializer := codecs.UniversalDeserializer()

	//Create Test Server and Create Validating Webhook for Cowboys in source cluster.
	testWebhook := webhookserver.WebhookServer{
		Response: v1.AdmissionResponse{
			Allowed: true,
		},
		ObjectGVK: schema.GroupVersionKind{
			Group:   "wildwest.dev",
			Version: "v1alpha1",
			Kind:    "Cowboy",
		},
		T:            t,
		Lock:         sync.Mutex{},
		Deserializer: deserializer,
	}

	port, err := framework.GetFreePort(t)
	require.NoError(t, err, "failed to get free port for test webhook")
	testWebhook.StartServer(ctx, server, port)

	t.Logf("Installing webhook into the source workspace")
	sideEffect := admissionregistrationv1.SideEffectClassNone
	webhook := &admissionregistrationv1.ValidatingWebhookConfiguration{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "test-webhook"},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{{
			Name: "test-webhook.cowboy.io",
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL:      testWebhook.GetURL(),
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
	_, err = kubeClusterClient.Cluster(sourceWorkspace).AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, webhook, metav1.CreateOptions{})
	require.NoError(t, err, "failed to add validating webhook configurations")

	cowboy := v1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "testing",
		},
		Spec: v1alpha1.CowboySpec{},
	}

	// Avoid race condition here by making sure that CRD is served after installing the types into logical clusters
	t.Logf("Creating cowboy resource in target logical cluster")
	require.Eventually(t, func() bool {
		_, err = cowbyClients.Cluster(targetWorkspace).WildwestV1alpha1().Cowboys("default").Create(ctx, &cowboy, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			return false
		}
		return testWebhook.Calls >= 1

	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}

func TestAPIBindingValidatingWebhookNotCalledWhenBound(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	sourceWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName, "Universal")
	targetWorkspace := framework.NewWorkspaceFixture(t, server, orgClusterName, "Universal")

	cfg := server.DefaultConfig(t)

	kcpClients, err := clientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClients, err := dynamic.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kubernetes.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")

	t.Logf("Install a cowboys APIResourceSchema into workspace %q", sourceWorkspace)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClients.Cluster(sourceWorkspace).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClients.Cluster(sourceWorkspace), mapper, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
		},
	}
	_, err = kcpClients.Cluster(sourceWorkspace).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in workspace %q that points to the today-cowboys export", targetWorkspace)
	require.NoError(t, err)
	apiBinding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					WorkspaceName: sourceWorkspace.Base(),
					ExportName:    cowboysAPIExport.Name,
				},
			},
		},
	}

	_, err = kcpClients.Cluster(targetWorkspace).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	scheme := runtime.NewScheme()
	err = admissionregistrationv1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission registration v1 scheme")
	err = v1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission v1 scheme")
	err = v1alpha1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add cowboy v1alpha1 to scheme")
	cowbyClients, err := client.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to add cowboy v1alpha1 to scheme")
	codecs := serializer.NewCodecFactory(scheme)
	deserializer := codecs.UniversalDeserializer()

	//Create Test Server and Create Validating Webhook for Cowboys in source cluster.
	testWebhook := webhookserver.WebhookServer{
		Response: v1.AdmissionResponse{
			Allowed: true,
		},
		ObjectGVK: schema.GroupVersionKind{
			Group:   "wildwest.dev",
			Version: "v1alpha1",
			Kind:    "Cowboy",
		},
		T:            t,
		Lock:         sync.Mutex{},
		Deserializer: deserializer,
	}

	port, err := framework.GetFreePort(t)
	require.NoError(t, err, "failed to get free port for test webhook")
	testWebhook.StartServer(ctx, server, port)

	t.Logf("Installing webhook into the source workspace")
	sideEffect := admissionregistrationv1.SideEffectClassNone
	webhook := &admissionregistrationv1.ValidatingWebhookConfiguration{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "test-webhook"},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{{
			Name: "test-webhook.cowboy.io",
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL:      testWebhook.GetURL(),
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
	_, err = kubeClusterClient.Cluster(targetWorkspace).AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, webhook, metav1.CreateOptions{})
	require.NoError(t, err, "failed to add validating webhook configurations")

	cowboy := v1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "testing",
		},
		Spec: v1alpha1.CowboySpec{},
	}

	// Avoid race condition here by making sure that CRD is served after installing the types into logical clusters
	t.Logf("Creating cowboy resource in target logical cluster")
	require.Eventually(t, func() bool {
		_, err = cowbyClients.Cluster(targetWorkspace).WildwestV1alpha1().Cowboys("default").Create(ctx, &cowboy, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			return false
		}
		return testWebhook.Calls == 0

	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}
