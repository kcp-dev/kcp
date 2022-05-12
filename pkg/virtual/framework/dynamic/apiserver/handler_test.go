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

package apiserver

import (
	"context"
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/controller/openapi/builder"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/protobuf"
	"k8s.io/apiserver/pkg/endpoints/handlers"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	utilopenapi "k8s.io/apiserver/pkg/util/openapi"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	apiDefs "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefs"
	apidefs "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefs"
)

type mockedAPISetRetriever apiDefs.APIDefinitionSet

var _ apiDefs.APIDefinitionSetGetter = (*mockedAPISetRetriever)(nil)

func (masr mockedAPISetRetriever) GetAPIDefinitionSet(apiDomainKey string) (apis apidefs.APIDefinitionSet, apisExist bool) {
	return apiDefs.APIDefinitionSet(masr), true
}

type mockedAPIDefinition struct {
	apiResourceSpec *v1alpha1.CommonAPIResourceSpec
}

var _ apiDefs.APIDefinition = (*mockedAPIDefinition)(nil)

func (apiDef *mockedAPIDefinition) GetAPIResourceSpec() *v1alpha1.CommonAPIResourceSpec {
	return apiDef.apiResourceSpec
}
func (apiDef *mockedAPIDefinition) GetClusterName() logicalcluster.Name {
	return logicalcluster.New("logicalClusterName")
}
func (apiDef *mockedAPIDefinition) GetStorage() rest.Storage {
	return nil
}
func (apiDef *mockedAPIDefinition) GetSubResourceStorage(subresource string) rest.Storage {
	return nil
}
func (apiDef *mockedAPIDefinition) GetRequestScope() *handlers.RequestScope {
	return nil
}
func (apiDef *mockedAPIDefinition) GetSubResourceRequestScope(subresource string) *handlers.RequestScope {
	return nil
}

func TestRouting(t *testing.T) {
	hasSynced := false

	apiSetRetriever := mockedAPISetRetriever{
		schema.GroupVersionResource{
			Group:    "custom",
			Version:  "v1",
			Resource: "customresources",
		}: &mockedAPIDefinition{
			apiResourceSpec: &v1alpha1.CommonAPIResourceSpec{
				GroupVersion: v1alpha1.GroupVersion{
					Group:   "custom",
					Version: "v1",
				},
				Scope: apiextensionsv1.NamespaceScoped,
				CustomResourceDefinitionNames: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "customresources",
					Singular: "customresource",
					Kind:     "CustomResource",
					ListKind: "CustomResourceList",
				},
			},
		},
		schema.GroupVersionResource{
			Group:    "",
			Version:  "v1",
			Resource: "services",
		}: &mockedAPIDefinition{
			apiResourceSpec: &v1alpha1.CommonAPIResourceSpec{
				GroupVersion: v1alpha1.GroupVersion{
					Group:   "",
					Version: "v1",
				},
				Scope: apiextensionsv1.NamespaceScoped,
				CustomResourceDefinitionNames: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "services",
					Singular: "service",
					Kind:     "Service",
					ListKind: "ServiceList",
				},
			},
		},
	}

	// note that in production we delegate to the special handler that is attached at the end of the delegation chain that checks if the server has installed all known HTTP paths before replying to the client.
	// it returns 503 if not all registered signals have been ready (closed) otherwise it simply replies with 404.
	// the apiextentionserver is considered to be initialized once hasCRDInformerSyncedSignal is closed.
	//
	// here, in this test the delegate represent the special handler and hasSync represents the signal.
	// primarily we just want to make sure that the delegate has been called.
	// the behaviour of the real delegate is tested elsewhere.
	delegateCalled := false
	delegate := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		delegateCalled = true
		if !hasSynced {
			http.Error(w, "", 503)
			return
		}
		http.Error(w, "", 418)
	})

	versionDiscoveryHandler := &versionDiscoveryHandler{
		apiSetRetriever: apiSetRetriever,
		delegate:        delegate,
	}
	groupDiscoveryHandler := &groupDiscoveryHandler{
		apiSetRetriever: apiSetRetriever,
		delegate:        delegate,
	}
	rootDiscoveryHandler := &rootDiscoveryHandler{
		apiSetRetriever: apiSetRetriever,
		delegate:        delegate,
	}

	handler := &resourceHandler{
		apiSetRetriever:         apiSetRetriever,
		delegate:                delegate,
		versionDiscoveryHandler: versionDiscoveryHandler,
		groupDiscoveryHandler:   groupDiscoveryHandler,
		rootDiscoveryHandler:    rootDiscoveryHandler,
	}

	testcases := []struct {
		Name    string
		Method  string
		Path    string
		Headers map[string]string
		Body    io.Reader

		APIGroup          string
		APIVersion        string
		Verb              string
		Resource          string
		IsResourceRequest bool

		HasSynced bool

		ExpectStatus         int
		ExpectResponse       func(*testing.T, *http.Response, []byte)
		ExpectDelegateCalled bool
	}{
		{
			Name:                 "existing group discovery, presync",
			Method:               "GET",
			Path:                 "/apis/custom",
			APIGroup:             "custom",
			APIVersion:           "",
			HasSynced:            false,
			IsResourceRequest:    false,
			ExpectDelegateCalled: false,
			ExpectStatus:         200,
			ExpectResponse: func(t *testing.T, r *http.Response, b []byte) {
				if r.Header.Get("Content-Type") == "application/json" && r.StatusCode == 200 {
					require.Equal(t,
						"{\"kind\":\"APIGroup\",\"apiVersion\":\"v1\",\"name\":\"custom\",\"versions\":[{\"groupVersion\":\"custom/v1\",\"version\":\"v1\"}],\"preferredVersion\":{\"groupVersion\":\"custom/v1\",\"version\":\"v1\"}}\n",
						string(b))
				}
			},
		},
		{
			Name:                 "existing group discovery",
			Method:               "GET",
			Path:                 "/apis/custom",
			APIGroup:             "custom",
			APIVersion:           "",
			HasSynced:            true,
			IsResourceRequest:    false,
			ExpectDelegateCalled: false,
			ExpectStatus:         200,
			ExpectResponse: func(t *testing.T, r *http.Response, b []byte) {
				if r.Header.Get("Content-Type") == "application/json" && r.StatusCode == 200 {
					require.Equal(t,
						"{\"kind\":\"APIGroup\",\"apiVersion\":\"v1\",\"name\":\"custom\",\"versions\":[{\"groupVersion\":\"custom/v1\",\"version\":\"v1\"}],\"preferredVersion\":{\"groupVersion\":\"custom/v1\",\"version\":\"v1\"}}\n",
						string(b))
				}
			},
		},

		{
			Name:                 "nonexisting group discovery, presync",
			Method:               "GET",
			Path:                 "/apis/other",
			APIGroup:             "other",
			APIVersion:           "",
			HasSynced:            false,
			IsResourceRequest:    false,
			ExpectDelegateCalled: true,
			ExpectStatus:         503,
		},
		{
			Name:                 "nonexisting group discovery",
			Method:               "GET",
			Path:                 "/apis/other",
			APIGroup:             "other",
			APIVersion:           "",
			HasSynced:            true,
			IsResourceRequest:    false,
			ExpectDelegateCalled: true,
			ExpectStatus:         418,
		},

		{
			Name:                 "existing group version discovery, presync",
			Method:               "GET",
			Path:                 "/apis/custom/v1",
			APIGroup:             "custom",
			APIVersion:           "v1",
			HasSynced:            false,
			IsResourceRequest:    false,
			ExpectDelegateCalled: false,
			ExpectStatus:         200,
			ExpectResponse: func(t *testing.T, r *http.Response, b []byte) {
				if r.Header.Get("Content-Type") == "application/json" && r.StatusCode == 200 {
					require.Equal(t,
						"{\"kind\":\"APIResourceList\",\"apiVersion\":\"v1\",\"groupVersion\":\"custom/v1\",\"resources\":[{\"name\":\"customresources\",\"singularName\":\"customresource\",\"namespaced\":true,\"kind\":\"CustomResource\",\"verbs\":[\"get\",\"list\",\"patch\",\"create\",\"update\",\"watch\"],\"storageVersionHash\":\"ixY6U/JU9OM=\"}]}\n",
						string(b))
				}
			},
		},
		{
			Name:                 "existing group version discovery",
			Method:               "GET",
			Path:                 "/apis/custom/v1",
			APIGroup:             "custom",
			APIVersion:           "v1",
			HasSynced:            true,
			IsResourceRequest:    false,
			ExpectDelegateCalled: false,
			ExpectStatus:         200,
			ExpectResponse: func(t *testing.T, r *http.Response, b []byte) {
				if r.Header.Get("Content-Type") == "application/json" && r.StatusCode == 200 {
					require.Equal(t,
						"{\"kind\":\"APIResourceList\",\"apiVersion\":\"v1\",\"groupVersion\":\"custom/v1\",\"resources\":[{\"name\":\"customresources\",\"singularName\":\"customresource\",\"namespaced\":true,\"kind\":\"CustomResource\",\"verbs\":[\"get\",\"list\",\"patch\",\"create\",\"update\",\"watch\"],\"storageVersionHash\":\"ixY6U/JU9OM=\"}]}\n",
						string(b))
				}
			},
		},

		{
			Name:                 "nonexisting group version discovery, presync",
			Method:               "GET",
			Path:                 "/apis/other/v1",
			APIGroup:             "other",
			APIVersion:           "v1",
			HasSynced:            false,
			IsResourceRequest:    false,
			ExpectDelegateCalled: true,
			ExpectStatus:         503,
		},
		{
			Name:                 "nonexisting group version discovery",
			Method:               "GET",
			Path:                 "/apis/other/v1",
			APIGroup:             "other",
			APIVersion:           "v1",
			HasSynced:            true,
			IsResourceRequest:    false,
			ExpectDelegateCalled: true,
			ExpectStatus:         418,
		},

		{
			Name:                 "existing group, nonexisting version discovery, presync",
			Method:               "GET",
			Path:                 "/apis/custom/v2",
			APIGroup:             "custom",
			APIVersion:           "v2",
			HasSynced:            false,
			IsResourceRequest:    false,
			ExpectDelegateCalled: true,
			ExpectStatus:         503,
		},
		{
			Name:                 "existing group, nonexisting version discovery",
			Method:               "GET",
			Path:                 "/apis/custom/v2",
			APIGroup:             "custom",
			APIVersion:           "v2",
			HasSynced:            true,
			IsResourceRequest:    false,
			ExpectDelegateCalled: true,
			ExpectStatus:         418,
		},

		{
			Name:                 "nonexisting group, resource request, presync",
			Method:               "GET",
			Path:                 "/apis/custom/v2/foos",
			APIGroup:             "custom",
			APIVersion:           "v2",
			Verb:                 "list",
			Resource:             "foos",
			HasSynced:            false,
			IsResourceRequest:    true,
			ExpectDelegateCalled: true,
			ExpectStatus:         503,
		},
		{
			Name:                 "nonexisting group, resource request",
			Method:               "GET",
			Path:                 "/apis/custom/v2/foos",
			APIGroup:             "custom",
			APIVersion:           "v2",
			Verb:                 "list",
			Resource:             "foos",
			HasSynced:            true,
			IsResourceRequest:    true,
			ExpectDelegateCalled: true,
			ExpectStatus:         418,
		},

		{
			Name:                 "existing core group discovery, presync",
			Method:               "GET",
			Path:                 "/api",
			APIGroup:             "",
			APIVersion:           "",
			HasSynced:            false,
			IsResourceRequest:    false,
			ExpectDelegateCalled: false,
			ExpectStatus:         200,
			ExpectResponse: func(t *testing.T, r *http.Response, b []byte) {
				if r.Header.Get("Content-Type") == "application/json" && r.StatusCode == 200 {
					require.Equal(t,
						"{\"kind\":\"APIGroup\",\"name\":\"\",\"versions\":[{\"groupVersion\":\"v1\",\"version\":\"v1\"}],\"preferredVersion\":{\"groupVersion\":\"v1\",\"version\":\"v1\"}}\n",
						string(b))
				}
			},
		},
		{
			Name:                 "existing core group discovery",
			Method:               "GET",
			Path:                 "/api",
			APIGroup:             "",
			APIVersion:           "",
			HasSynced:            true,
			IsResourceRequest:    false,
			ExpectDelegateCalled: false,
			ExpectStatus:         200,
			ExpectResponse: func(t *testing.T, r *http.Response, b []byte) {
				if r.Header.Get("Content-Type") == "application/json" && r.StatusCode == 200 {
					require.Equal(t,
						"{\"kind\":\"APIGroup\",\"name\":\"\",\"versions\":[{\"groupVersion\":\"v1\",\"version\":\"v1\"}],\"preferredVersion\":{\"groupVersion\":\"v1\",\"version\":\"v1\"}}\n",
						string(b))
				}
			},
		},
		{
			Name:                 "existing core group version discovery, presync",
			Method:               "GET",
			Path:                 "/api/v1",
			APIGroup:             "",
			APIVersion:           "v1",
			HasSynced:            false,
			IsResourceRequest:    false,
			ExpectDelegateCalled: false,
			ExpectStatus:         200,
			ExpectResponse: func(t *testing.T, r *http.Response, b []byte) {
				if r.Header.Get("Content-Type") == "application/json" && r.StatusCode == 200 {
					require.Equal(t,
						"{\"kind\":\"APIResourceList\",\"groupVersion\":\"v1\",\"resources\":[{\"name\":\"services\",\"singularName\":\"service\",\"namespaced\":true,\"kind\":\"Service\",\"verbs\":[\"get\",\"list\",\"patch\",\"create\",\"update\",\"watch\"],\"storageVersionHash\":\"+iYBRzoiY8o=\"}]}\n",
						string(b))
				}
			},
		},
		{
			Name:                 "existing core group version discovery",
			Method:               "GET",
			Path:                 "/api/v1",
			APIGroup:             "",
			APIVersion:           "v1",
			HasSynced:            true,
			IsResourceRequest:    false,
			ExpectDelegateCalled: false,
			ExpectStatus:         200,
			ExpectResponse: func(t *testing.T, r *http.Response, b []byte) {
				if r.Header.Get("Content-Type") == "application/json" && r.StatusCode == 200 {
					require.Equal(t,
						"{\"kind\":\"APIResourceList\",\"groupVersion\":\"v1\",\"resources\":[{\"name\":\"services\",\"singularName\":\"service\",\"namespaced\":true,\"kind\":\"Service\",\"verbs\":[\"get\",\"list\",\"patch\",\"create\",\"update\",\"watch\"],\"storageVersionHash\":\"+iYBRzoiY8o=\"}]}\n",
						string(b))
				}
			},
		},
		{
			Name:                 "existing core group, nonexisting version discovery, presync",
			Method:               "GET",
			Path:                 "/api/v2",
			APIGroup:             "",
			APIVersion:           "v2",
			HasSynced:            false,
			IsResourceRequest:    false,
			ExpectDelegateCalled: true,
			ExpectStatus:         503,
		},
		{
			Name:                 "existing core group, nonexisting version discovery",
			Method:               "GET",
			Path:                 "/api/v2",
			APIGroup:             "",
			APIVersion:           "v2",
			HasSynced:            true,
			IsResourceRequest:    false,
			ExpectDelegateCalled: true,
			ExpectStatus:         418,
		},

		{
			Name:                 "nonexisting core group, resource request, presync",
			Method:               "GET",
			Path:                 "/api/v2/foos",
			APIGroup:             "",
			APIVersion:           "v2",
			Verb:                 "list",
			Resource:             "foos",
			HasSynced:            false,
			IsResourceRequest:    true,
			ExpectDelegateCalled: true,
			ExpectStatus:         503,
		},
		{
			Name:                 "nonexisting core group, resource request",
			Method:               "GET",
			Path:                 "/api/v2/foos",
			APIGroup:             "",
			APIVersion:           "v2",
			Verb:                 "list",
			Resource:             "foos",
			HasSynced:            true,
			IsResourceRequest:    true,
			ExpectDelegateCalled: true,
			ExpectStatus:         418,
		},
		{
			Name:                 "existing group, resource request",
			Method:               "GET",
			Path:                 "/apis/custom/v1/customresources",
			APIGroup:             "custom",
			APIVersion:           "v1",
			Verb:                 "list",
			Resource:             "customresources",
			HasSynced:            true,
			IsResourceRequest:    true,
			ExpectDelegateCalled: false,
			ExpectStatus:         405,
			ExpectResponse: func(t *testing.T, r *http.Response, b []byte) {
				if r.Header.Get("Content-Type") == "application/json" {
					require.JSONEq(t,
						"{\"kind\":\"Status\",\"apiVersion\":\"v1\",\"metadata\":{},\"status\":\"Failure\",\"message\":\"list is not supported on resources of kind \\\"customresources.custom\\\"\",\"reason\":\"MethodNotAllowed\",\"details\":{\"group\":\"custom\",\"kind\":\"customresources\"},\"code\":405}\n",
						string(b))
				}
			},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.Name, func(t *testing.T) {
			for _, contentType := range []string{"json", "yaml", "proto", "unknown"} {
				t.Run(contentType, func(t *testing.T) {
					delegateCalled = false
					hasSynced = tc.HasSynced

					recorder := httptest.NewRecorder()

					req := httptest.NewRequest(tc.Method, tc.Path, tc.Body)
					for k, v := range tc.Headers {
						req.Header.Set(k, v)
					}

					expectStatus := tc.ExpectStatus
					switch contentType {
					case "json":
						req.Header.Set("Accept", "application/json")
					case "yaml":
						req.Header.Set("Accept", "application/yaml")
					case "proto":
						req.Header.Set("Accept", "application/vnd.kubernetes.protobuf, application/json")
					case "unknown":
						req.Header.Set("Accept", "application/vnd.kubernetes.unknown")
						// rather than success, we'll get a not supported error
						if expectStatus == 200 {
							expectStatus = 406
						}
					default:
						t.Fatalf("unknown content type %v", contentType)
					}

					req = req.WithContext(apirequest.WithRequestInfo(
						context.WithValue(req.Context(), apiDefs.APIDomainKeyContextKey, "domain"),
						&apirequest.RequestInfo{
							Verb:              tc.Verb,
							Resource:          tc.Resource,
							APIGroup:          tc.APIGroup,
							APIVersion:        tc.APIVersion,
							IsResourceRequest: tc.IsResourceRequest,
							Path:              tc.Path,
						}))

					handler.ServeHTTP(recorder, req)

					if tc.ExpectDelegateCalled != delegateCalled {
						t.Errorf("expected delegated called %v, got %v", tc.ExpectDelegateCalled, delegateCalled)
					}
					result := recorder.Result()
					content, _ := ioutil.ReadAll(result.Body)
					if e, a := expectStatus, result.StatusCode; e != a {
						t.Log(string(content))
						t.Errorf("expected %v, got %v", e, a)
					}
					if tc.ExpectResponse != nil {
						tc.ExpectResponse(t, result, content)
					}

					// Make sure error responses come back with status objects in all encodings, including unknown encodings
					if !delegateCalled && expectStatus >= 300 {
						status := &metav1.Status{}

						switch contentType {
						// unknown accept headers fall back to json errors
						case "json", "unknown":
							if e, a := "application/json", result.Header.Get("Content-Type"); e != a {
								t.Errorf("expected Content-Type %v, got %v", e, a)
							}
							if err := json.Unmarshal(content, status); err != nil {
								t.Fatal(err)
							}
						case "yaml":
							if e, a := "application/yaml", result.Header.Get("Content-Type"); e != a {
								t.Errorf("expected Content-Type %v, got %v", e, a)
							}
							if err := yaml.Unmarshal(content, status); err != nil {
								t.Fatal(err)
							}
						case "proto":
							if e, a := "application/vnd.kubernetes.protobuf", result.Header.Get("Content-Type"); e != a {
								t.Errorf("expected Content-Type %v, got %v", e, a)
							}
							if _, _, err := protobuf.NewSerializer(scheme, scheme).Decode(content, nil, status); err != nil {
								t.Fatal(err)
							}
						default:
							t.Fatalf("unknown content type %v", contentType)
						}

						if e, a := metav1.Unversioned.WithKind("Status"), status.GroupVersionKind(); e != a {
							t.Errorf("expected %#v, got %#v", e, a)
						}
						if int(status.Code) != int(expectStatus) {
							t.Errorf("expected %v, got %v", expectStatus, status.Code)
						}
					}
				})
			}
		})
	}
}

func exampleAPIResourceSpec() *v1alpha1.CommonAPIResourceSpec {
	return &v1alpha1.CommonAPIResourceSpec{
		GroupVersion: v1alpha1.GroupVersion{
			Group:   "stable.example.com",
			Version: "v1beta1",
		},
		CustomResourceDefinitionNames: apiextensionsv1.CustomResourceDefinitionNames{
			Plural: "examples", Singular: "example", Kind: "Example", ShortNames: []string{"ex"}, ListKind: "ExampleList", Categories: []string{"all"},
		},
		Scope: apiextensionsv1.ClusterScoped,
		SubResources: v1alpha1.SubResources{
			v1alpha1.SubResource{
				Name: "status",
			},
		},
	}
}

func TestBuildOpenAPIModelsForApply(t *testing.T) {
	// This is a list of validation that we expect to work.
	tests := []apiextensionsv1.CustomResourceValidation{
		{
			OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
				Type:       "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{"num": {Type: "integer", Description: "v1beta1 num field"}},
			},
		},
		{
			OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
				Type:         "",
				XIntOrString: true,
			},
		},
		{
			OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"oneOf": {
						OneOf: []apiextensionsv1.JSONSchemaProps{
							{Type: "boolean"},
							{Type: "string"},
						},
					},
				},
			},
		},
		{
			OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"nullable": {
						Type:     "integer",
						Nullable: true,
					},
				},
			},
		},
	}

	spec := exampleAPIResourceSpec()

	for i, test := range tests {
		_ = spec.SetSchema(test.OpenAPIV3Schema)
		swagger, err := buildOpenAPIV2(spec, builder.Options{V2: true, SkipFilterSchemaForKubectlOpenAPIV2Validation: true, StripValueValidation: true, StripNullable: true, AllowNonStructural: false})
		require.NoError(t, err)

		openAPIModels, err := utilopenapi.ToProtoModels(swagger)
		if err != nil {
			t.Fatalf("failed to convert to apply model: %v", err)
		}
		if openAPIModels == nil {
			t.Fatalf("%d: failed to convert to apply model: nil", i)
		}
	}
}
