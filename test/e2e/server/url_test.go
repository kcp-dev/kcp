/*
Copyright 2025 The KCP Authors.

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

package server

import (
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/sdk/apis/core"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestURLs(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")
	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	transport, err := rest.TransportFor(cfg)
	require.NoError(t, err)
	httpClient := http.Client{
		Transport: transport,
	}

	testPaths := map[string]struct {
		path               string
		expectedStatusCode int
	}{
		"a well-formed URL does not error": {
			path:               orgPath.RequestPath() + "/apis/core.kcp.io/v1alpha1/logicalclusters/cluster/status",
			expectedStatusCode: http.StatusOK,
		},
		"a URL with two clusters does error": {
			path:               orgPath.RequestPath() + orgPath.RequestPath() + "/apis/core.kcp.io/v1alpha1/logicalclusters/cluster/status",
			expectedStatusCode: http.StatusNotFound,
		},
		"a URL with three clusters does error": {
			path:               orgPath.RequestPath() + orgPath.RequestPath() + orgPath.RequestPath() + "/apis/core.kcp.io/v1alpha1/logicalclusters/cluster/status",
			expectedStatusCode: http.StatusNotFound,
		},
	}

	for testName, testPath := range testPaths {
		t.Run(testName, func(t *testing.T) {
			t.Parallel()

			u, err := url.Parse(cfg.Host)
			require.NoError(t, err)
			u.Path = testPath.path

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u.String(), http.NoBody)
			require.NoError(t, err)

			t.Logf("Testing URL: %s", req.URL.String())
			resp, err := httpClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			if !assert.Equal(t, testPath.expectedStatusCode, resp.StatusCode) {
				t.Logf("Expected status code %d, got %d", testPath.expectedStatusCode, resp.StatusCode)
				b, err := io.ReadAll(resp.Body)
				assert.NoError(t, err)
				t.Logf("Response body: %s", string(b))
			}
		})
	}
}

func TestURLWithClusterKind(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")
	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	for _, scope := range []apiextensionsv1.ResourceScope{
		apiextensionsv1.ClusterScoped,
		apiextensionsv1.NamespaceScoped,
	} {
		t.Run(string(scope), func(t *testing.T) {
			t.Parallel()

			orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

			crdClient := kcpapiextensionsclientset.NewForConfigOrDie(cfg)
			dynamicClient, err := kcpdynamic.NewForConfig(cfg)
			require.NoError(t, err)

			t.Log("Install a CRD with kind Cluster")
			crd := &apiextensionsv1.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "clusters.url.test",
				},
				Spec: apiextensionsv1.CustomResourceDefinitionSpec{
					Group: "url.test",
					Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
						{
							Name:    "v1",
							Served:  true,
							Storage: true,
							Schema: &apiextensionsv1.CustomResourceValidation{
								OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
									Type: "object",
								},
							},
							Subresources: &apiextensionsv1.CustomResourceSubresources{
								Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
							},
						},
					},
					Scope: scope,
					Names: apiextensionsv1.CustomResourceDefinitionNames{
						Plural:   "clusters",
						Singular: "cluster",
						Kind:     "Cluster",
						ListKind: "ClusterList",
					},
				},
			}
			err = configcrds.CreateSingle(t.Context(), crdClient.Cluster(orgPath).ApiextensionsV1().CustomResourceDefinitions(), crd)
			require.NoError(t, err)

			t.Log("Create a resource to retrieve")
			gvr := schema.GroupVersionResource{
				Group:    "url.test",
				Version:  "v1",
				Resource: "clusters",
			}

			obj := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": gvr.Group + "/v1",
					"kind":       "Cluster",
					"metadata": map[string]interface{}{
						"name": "test",
					},
				},
			}

			var resourceInterface dynamic.ResourceInterface
			if scope == apiextensionsv1.ClusterScoped {
				resourceInterface = dynamicClient.Cluster(orgPath).Resource(gvr)
			} else {
				resourceInterface = dynamicClient.Cluster(orgPath).Resource(gvr).Namespace("default")
			}
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err = resourceInterface.Create(t.Context(), obj, metav1.CreateOptions{})
				if err != nil {
					return false, err.Error()
				}
				return true, ""
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Log("Retrieve the resource")
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err = resourceInterface.Get(t.Context(), "test", metav1.GetOptions{})
				if err != nil {
					return false, err.Error()
				}
				return true, ""
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			t.Log("Retrieve the status subresource")
			kcptestinghelpers.Eventually(t, func() (bool, string) {
				_, err = resourceInterface.Get(t.Context(), "test", metav1.GetOptions{}, "status")
				if err != nil {
					return false, err.Error()
				}
				return true, ""
			}, wait.ForeverTestTimeout, time.Millisecond*100)
		})
	}
}
