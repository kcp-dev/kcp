/*
Copyright 2023 The KCP Authors.

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

package apilifecycle

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/logicalcluster/v3"
)

// WebhookResponse describes the webhook response as an array of objects
type WebhookResponse []WebhookObject

// WebhookObject is both the Kubernetes object and the resource corresponding to this object
type WebhookObject struct {
	// Resource corresponding to the object
	Resource string `json:"resource"`

	// Object is a Kubernetes Object
	Object map[string]interface{} `json:"object"`
}

func (c *controller) reconcile(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) (bool, error) {
	logger := klog.FromContext(ctx)

	// Check for valid reference
	exportRef := apiBinding.Spec.Reference.Export
	if exportRef == nil {
		// Should not happened because of OpenAPI
		logger.Info("missing APIExport reference")

		// error handled by bindingReconciler - recoverable
		return true, nil
	}

	// Get APIExport
	apiExportPath := logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path)
	if apiExportPath.Empty() {
		apiExportPath = logicalcluster.From(apiBinding).Path()
	}

	apiExport, err := c.getAPIExport(apiExportPath, exportRef.Name)
	if err != nil {
		logger.Error(err, "error getting APIExport", "path", apiExportPath, "name", exportRef.Name)
		// error handled by bindingReconciler - recoverable
		return true, nil
	}

	if len(apiExport.Status.VirtualWorkspaces) == 0 {
		logger.Info("view is not ready yet")
		// View is not ready yet. Requeue
		return true, nil
	}

	// Get APILifecycle hooks
	lifecycles, err := c.getAPILifecycleByAPIExport(apiExport)
	if err != nil {
		logger.Error(err, "error getting APILifecycle objects for APIExport", "path", apiExportPath, "name", exportRef.Name)

		// recoverable
		return true, err
	}

	// Stop here if no hooks
	if len(lifecycles) == 0 {
		return false, nil
	}

	config, err := c.getAPIExportAdminConfig(ctx, apiExport)
	if err != nil || config == nil {
		return true, err
	}

	dynamicClusterClient, err := kcpdynamic.NewForConfig(config)
	if err != nil {
		logger.Error(err, "failed to create dynamic cluster client")
		return true, err
	}

	bindingCluster := logicalcluster.From(apiBinding)

	for _, lifecycle := range lifecycles {
		if lifecycle.Spec.Hooks.Bind != nil {
			// TODO: report the last invocations
			// TODO: may generate a kube event per invocation

			manifests, err := c.invokeHook(ctx, lifecycle.Spec.Hooks.Bind.URL)
			if err != nil {
				// TODO: retry policies, timeout, etc...

				// recoverable?
				return true, err
			}

			for _, manifest := range manifests {
				u := unstructured.Unstructured{Object: manifest.Object}

				// TODO: use Apply when upgrading to k8s 1.25
				_, err := dynamicClusterClient.
					Resource(u.GroupVersionKind().GroupVersion().WithResource(manifest.Resource)).
					Cluster(bindingCluster.Path()).
					Namespace("default").
					Create(ctx, &u, metav1.CreateOptions{})

				if errors.IsAlreadyExists(err) {
					_, err = dynamicClusterClient.
						Resource(u.GroupVersionKind().GroupVersion().WithResource(manifest.Resource)).
						Cluster(bindingCluster.Path()).
						Namespace("default").
						Update(ctx, &u, metav1.UpdateOptions{})
				}

				if err != nil {
					return true, err
				}
			}
		}
	}

	return false, nil
}

// getAPIExportAdminConfig returns a REST config
func (c *controller) getAPIExportAdminConfig(ctx context.Context, apiExport *apisv1alpha1.APIExport) (*rest.Config, error) {
	logger := klog.FromContext(ctx)

	exportCluster := logicalcluster.From(apiExport)

	// Read admin user secret
	kubeClusterClient := kcpkubernetesclientset.NewForConfigOrDie(c.config)
	secrets, err := kubeClusterClient.CoreV1().Secrets().Cluster(exportCluster.Path()).Namespace("default").List(ctx, metav1.ListOptions{})
	if err != nil {
		logger.Error(err, "failed to list secrets")
		return nil, err
	}

	var defaultSecret *corev1.Secret
	for _, secret := range secrets.Items {
		if secret.Type == "kubernetes.io/service-account-token" && strings.HasPrefix(secret.Name, "default-token") {
			defaultSecret = &secret
			break
		}
	}

	if defaultSecret == nil {
		logger.Info("default token not found")
		return nil, nil
	}

	apiexportConfig := rest.CopyConfig(c.config)
	apiexportConfig.Host = apiExport.Status.VirtualWorkspaces[0].URL // TODO: use APIExportEndpointSlice
	apiexportConfig.BearerToken = string(defaultSecret.Data["token"])
	apiexportConfig.CAData = defaultSecret.Data["ca.crt"]

	return apiexportConfig, nil
}

func (c *controller) invokeHook(ctx context.Context, url string) (WebhookResponse, error) {
	logger := klog.FromContext(ctx)

	// TODO: determine input parameters
	body := "{}"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Error(err, "error calling webhook")
		return nil, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		logger.Info("webhook did not return 200 status code", "code", resp.StatusCode, "body", string(b))
		return nil, nil
	}

	var manifests WebhookResponse
	err = json.Unmarshal(b, &manifests)
	if err != nil {
		logger.Error(err, "failed to unmarshal bytes to JSON")
		return nil, err
	}

	return manifests, nil
}
