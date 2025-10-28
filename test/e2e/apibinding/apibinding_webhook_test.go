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
	"crypto/tls"
	"fmt"
	gohttp "net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	admissionv1 "k8s.io/api/admission/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/kcp/sdk/testing/server"
	webhookserver "github.com/kcp-dev/kcp/test/e2e/fixtures/webhook"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIBindingMutatingWebhook(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx := t.Context()

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	sourcePath, sourceWS := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	targetPath, targetWS := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")

	sourceWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Install a cowboys APIResourceSchema into workspace %q", sourcePath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(sourceWorkspaceClient.Cluster(sourcePath).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(sourcePath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "wildwest.dev",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(sourcePath).ApisV1alpha2().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in workspace %q that points to the today-cowboys export", targetPath)
	require.NoError(t, err)
	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: sourcePath.String(),
					Name: cowboysAPIExport.Name,
				},
			},
		},
	}

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(targetPath).ApisV1alpha2().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	scheme := runtime.NewScheme()
	err = admissionregistrationv1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission registration v1 scheme")
	err = admissionv1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission v1 scheme")
	err = v1alpha1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add cowboy v1alpha1 to scheme")
	cowbyClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to add cowboy v1alpha1 to scheme")
	codecs := serializer.NewCodecFactory(scheme)
	deserializer := codecs.UniversalDeserializer()

	t.Logf("Create test server and create mutating webhook for cowboys in both source and target cluster")
	var clusterInReviewObject atomic.Value
	testWebhooks := map[logicalcluster.Path]*webhookserver.AdmissionWebhookServer{}
	for _, cluster := range []logicalcluster.Path{sourcePath, targetPath} {
		testWebhooks[cluster] = &webhookserver.AdmissionWebhookServer{
			ResponseFn: func(obj runtime.Object, review *admissionv1.AdmissionReview) (*admissionv1.AdmissionResponse, error) {
				clusterInReviewObject.Store(logicalcluster.From(obj.(*v1alpha1.Cowboy)).String())
				return &admissionv1.AdmissionResponse{Allowed: true}, nil
			},
			ObjectGVK: schema.GroupVersionKind{
				Group:   "wildwest.dev",
				Version: "v1alpha1",
				Kind:    "Cowboy",
			},
			Deserializer: deserializer,
		}
		port, err := kcptestingserver.GetFreePort(t)
		require.NoError(t, err, "failed to get free port for test webhook")
		dirPath := filepath.Dir(server.KubeconfigPath())
		testWebhooks[cluster].StartTLS(t, filepath.Join(dirPath, "apiserver.crt"), filepath.Join(dirPath, "apiserver.key"), cfg.Host, port)

		sideEffect := admissionregistrationv1.SideEffectClassNone
		url := testWebhooks[cluster].GetURL()
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
		_, err = kubeClusterClient.Cluster(cluster).AdmissionregistrationV1().MutatingWebhookConfigurations().Create(ctx, webhook, metav1.CreateOptions{})
		require.NoError(t, err, "failed to add validating webhook configurations")
	}

	cowboy := v1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "testing",
		},
		Spec: v1alpha1.CowboySpec{},
	}

	// Avoid race condition here by making sure that CRD is served after installing the types into logical clusters
	t.Logf("Creating cowboy resource in target logical cluster")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := cowbyClusterClient.Cluster(targetPath).WildwestV1alpha1().Cowboys("default").Create(ctx, &cowboy, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return false, err.Error()
		}
		return testWebhooks[sourcePath].Calls() >= 1, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to create cowboy resource")

	t.Logf("Check that the logicalcluster annotation on the object that triggered webhook is matching the target cluster")
	require.Equal(t, targetWS.Spec.Cluster, clusterInReviewObject.Load(), "expected that the object passed to the webhook has correct kcp.io/cluster annotation set")

	t.Logf("Create an APIBinding in workspace %q that points to the today-cowboys export", sourcePath)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(sourcePath).ApisV1alpha2().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Ensure cowboys are served in %q", sourcePath)
	require.Eventually(t, func() bool {
		_, err := cowbyClusterClient.Cluster(sourcePath).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	t.Logf("Cowboys are served")

	sourceWHCalls := testWebhooks[sourcePath].Calls()

	t.Logf("Creating cowboy resource in source logical cluster, eventually going through admission webhook")
	require.Eventually(t, func() bool {
		_, err = cowbyClusterClient.Cluster(sourcePath).WildwestV1alpha1().Cowboys("default").Create(ctx, &cowboy, metav1.CreateOptions{})
		require.NoError(t, err)
		return testWebhooks[sourcePath].Calls() > sourceWHCalls
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Check that the logicalcluster annotation on the object that triggered webhook is matching the source cluster")
	require.Equal(t, sourceWS.Spec.Cluster, clusterInReviewObject.Load(), "expected that the object passed to the webhook has correct kcp.io/cluster annotation set")

	t.Logf("Check that the in-workspace webhook was NOT called")
	require.Zero(t, testWebhooks[targetPath].Calls(), "in-workspace webhook should not have been called")
}

func TestAPIBindingValidatingWebhook(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx := t.Context()

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	sourcePath, sourceWS := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	targetPath, targetWS := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	cfg := server.BaseConfig(t)

	kcpClients, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")

	sourceWorkspaceClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Install a cowboys APIResourceSchema into workspace %q", sourcePath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(sourceWorkspaceClient.Cluster(sourcePath).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(sourcePath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create an APIExport for it")
	cowboysAPIExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "today-cowboys",
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "wildwest.dev",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	_, err = kcpClients.Cluster(sourcePath).ApisV1alpha2().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in workspace %q that points to the today-cowboys export", targetPath)
	require.NoError(t, err)
	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: sourcePath.String(),
					Name: cowboysAPIExport.Name,
				},
			},
		},
	}

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClients.Cluster(targetPath).ApisV1alpha2().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	scheme := runtime.NewScheme()
	err = admissionregistrationv1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission registration v1 scheme")
	err = admissionv1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add admission v1 scheme")
	err = v1alpha1.AddToScheme(scheme)
	require.NoError(t, err, "failed to add cowboy v1alpha1 to scheme")
	cowbyClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to add cowboy v1alpha1 to scheme")
	codecs := serializer.NewCodecFactory(scheme)
	deserializer := codecs.UniversalDeserializer()

	t.Logf("Create test server and create validating webhook for cowboys in both source and target cluster")
	testWebhooks := map[logicalcluster.Path]*webhookserver.AdmissionWebhookServer{}
	var clusterInReviewObject atomic.Value
	for _, cluster := range []logicalcluster.Path{sourcePath, targetPath} {
		testWebhooks[cluster] = &webhookserver.AdmissionWebhookServer{
			ResponseFn: func(obj runtime.Object, review *admissionv1.AdmissionReview) (*admissionv1.AdmissionResponse, error) {
				clusterInReviewObject.Store(logicalcluster.From(obj.(*v1alpha1.Cowboy)).String())
				return &admissionv1.AdmissionResponse{Allowed: true}, nil
			},
			ObjectGVK: schema.GroupVersionKind{
				Group:   "wildwest.dev",
				Version: "v1alpha1",
				Kind:    "Cowboy",
			},
			Deserializer: deserializer,
		}
		port, err := kcptestingserver.GetFreePort(t)
		require.NoError(t, err, "failed to get free port for test webhook")
		dirPath := filepath.Dir(server.KubeconfigPath())
		testWebhooks[cluster].StartTLS(t, filepath.Join(dirPath, "apiserver.crt"), filepath.Join(dirPath, "apiserver.key"), cfg.Host, port)

		kcptestinghelpers.Eventually(t, func() (bool, string) {
			cl := gohttp.Client{Transport: &gohttp.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
			resp, err := cl.Get(testWebhooks[cluster].GetURL()) //nolint:noctx
			if err != nil {
				return false, err.Error()
			}
			resp.Body.Close()
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "failed to connect to webhook")

		sideEffect := admissionregistrationv1.SideEffectClassNone
		url := testWebhooks[cluster].GetURL()
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
		_, err = kubeClusterClient.Cluster(cluster).AdmissionregistrationV1().ValidatingWebhookConfigurations().Create(ctx, webhook, metav1.CreateOptions{})
		require.NoError(t, err, "failed to add validating webhook configurations")
	}

	cowboy := v1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "testing",
		},
		Spec: v1alpha1.CowboySpec{},
	}

	t.Logf("Ensure cowboys are served")
	require.Eventually(t, func() bool {
		_, err := cowbyClusterClient.Cluster(targetPath).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Creating cowboy resource in target logical cluster, eventually going through admission webhook")
	require.Eventually(t, func() bool {
		_, err = cowbyClusterClient.Cluster(targetPath).WildwestV1alpha1().Cowboys("default").Create(ctx, &cowboy, metav1.CreateOptions{})
		require.NoError(t, err)
		return testWebhooks[sourcePath].Calls() >= 1
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Check that the logicalcluster annotation on the object that triggered webhook is matching the target cluster")
	require.Equal(t, targetWS.Spec.Cluster, clusterInReviewObject.Load(), "expected that the object passed to the webhook has correct kcp.io/cluster annotation set")

	t.Logf("Create an APIBinding in workspace %q that points to the today-cowboys export", sourcePath)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClients.Cluster(sourcePath).ApisV1alpha2().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Ensure cowboys are served in %q", sourcePath)
	require.Eventually(t, func() bool {
		_, err := cowbyClusterClient.Cluster(sourcePath).WildwestV1alpha1().Cowboys("default").List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	t.Logf("Cowboys are served")

	sourceWHCalls := testWebhooks[sourcePath].Calls()

	t.Logf("Creating cowboy resource in source logical cluster, eventually going through admission webhook")
	require.Eventually(t, func() bool {
		_, err = cowbyClusterClient.Cluster(sourcePath).WildwestV1alpha1().Cowboys("default").Create(ctx, &cowboy, metav1.CreateOptions{})
		require.NoError(t, err)
		return testWebhooks[sourcePath].Calls() > sourceWHCalls
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Check that the logicalcluster annotation on the object that triggered webhook is matching the source cluster")
	require.Equal(t, sourceWS.Spec.Cluster, clusterInReviewObject.Load(), "expected that the object passed to the webhook has correct kcp.io/cluster annotation set")

	t.Logf("Check that the in-workspace webhook was NOT called")
	require.Zero(t, testWebhooks[targetPath].Calls(), "in-workspace webhook should not have been called")
}
