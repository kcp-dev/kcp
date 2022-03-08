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

package framework

// TODO: move non-e2e tests out of e2e package

/*
import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	openapi_v2 "github.com/googleapis/gnostic/openapiv2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	openapinamer "k8s.io/apiserver/pkg/endpoints/openapi"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/kubernetes"
	"k8s.io/kube-openapi/pkg/common"
	"k8s.io/kube-openapi/pkg/util"
	"k8s.io/kube-openapi/pkg/validation/spec"
	generatedopenapi "k8s.io/kubernetes/pkg/generated/openapi"

	virtualcmd "github.com/kcp-dev/kcp/pkg/virtual/framework/cmd"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/virtual/framework/compositecmd"
	"github.com/kcp-dev/kcp/test/e2e/virtual/helpers"
)

type virtualWorkspacesDescription []virtualWorkspaceDescription

func (d virtualWorkspacesDescription) ToRestStorages() map[string]map[schema.GroupVersion]map[string]rest.Storage {
	storages := make(map[string]map[schema.GroupVersion]map[string]rest.Storage)
	for _, vw := range d {
		storages[vw.prefix] = vw.ToRestStorages()
	}
	return storages
}

func (d virtualWorkspacesDescription) GetOpenAPIDefinitions() map[string]map[schema.GroupVersion]common.GetOpenAPIDefinitions {
	openAPIDefinitions := make(map[string]map[schema.GroupVersion]common.GetOpenAPIDefinitions)
	for _, vw := range d {
		openAPIDefinitions[vw.prefix] = vw.GetOpenAPIDefinitions()
	}
	return openAPIDefinitions
}

type virtualWorkspaceDescription struct {
	prefix string
	groups []virtualWorkspaceAPIGroupDescription
}

func (d virtualWorkspaceDescription) ToRestStorages() map[schema.GroupVersion]map[string]rest.Storage {
	storages := make(map[schema.GroupVersion]map[string]rest.Storage)
	for _, group := range d.groups {
		group := group
		storages[group.gv] = group.ToRestStorages()
	}
	return storages
}

func (d virtualWorkspaceDescription) GetOpenAPIDefinitions() map[schema.GroupVersion]common.GetOpenAPIDefinitions {
	openAPIDefinitions := make(map[schema.GroupVersion]common.GetOpenAPIDefinitions)
	for _, group := range d.groups {
		group := group
		openAPIDefinitions[group.gv] = func(callback common.ReferenceCallback) map[string]common.OpenAPIDefinition {
			gvDefs := group.GetOpenAPIDefinitions()
			for key, def := range generatedopenapi.GetOpenAPIDefinitions(callback) {
				gvDefs[key] = def
			}
			gvDefs[util.GetCanonicalTypeName(unstructured.UnstructuredList{})] = common.OpenAPIDefinition{}
			return gvDefs
		}
	}
	return openAPIDefinitions
}

type virtualWorkspaceAPIGroupDescription struct {
	gv        schema.GroupVersion
	resources []func() virtualWorkspaceResourceDescription
}
type virtualWorkspaceResourceDescription struct {
	namespaceScoped      bool
	allowCreateAndDelete bool
	new                  func() runtime.Unstructured
}

func (d virtualWorkspaceResourceDescription) Kind() string {
	return d.Type().Name()
}

func (d virtualWorkspaceResourceDescription) Type() reflect.Type {
	return reflect.TypeOf(d.new()).Elem()
}

func (d virtualWorkspaceResourceDescription) ResourceName() string {
	return strings.ToLower(d.Kind())
}

func (d virtualWorkspaceResourceDescription) OpenAPITypeName() string {
	return util.GetCanonicalTypeName(d.new())
}

func (d virtualWorkspaceResourceDescription) OpenAPISchemaDescription() string {
	return d.Kind() + " description"
}

func (d virtualWorkspaceAPIGroupDescription) ToRestStorages() map[string]rest.Storage {
	storages := make(map[string]rest.Storage)
	for _, resource := range d.resources {
		resource := resource()
		resourceName := resource.ResourceName()
		basicStorage := &compositecmd.BasicStorage{GVK: d.gv.WithKind(resource.Kind()), IsNamespaceScoped: resource.namespaceScoped, Creator: resource.new}
		if resource.allowCreateAndDelete {
			type readWriteRest struct {
				*compositecmd.BasicStorage
				*compositecmd.Creater
				*compositecmd.Deleter
			}
			storages[resourceName] = &readWriteRest{
				BasicStorage: basicStorage,
			}
		} else {
			storages[resourceName] = basicStorage
		}
	}
	return storages
}

func (d virtualWorkspaceAPIGroupDescription) GetOpenAPIDefinitions() map[string]common.OpenAPIDefinition {
	openAPIDefinitions := make(map[string]common.OpenAPIDefinition)
	for _, resource := range d.resources {
		resource := resource()
		canonicalTypeName := resource.OpenAPITypeName()
		openAPIDefinitions[canonicalTypeName] = common.OpenAPIDefinition{
			Schema: spec.Schema{
				SchemaProps: spec.SchemaProps{
					Description: resource.OpenAPISchemaDescription(),
					Type:        []string{"object"},
					Properties:  map[string]spec.Schema{},
				},
			},
		}
	}
	return openAPIDefinitions
}

func TestCompositeVirtualWorkspace(t *testing.T) {
	t.Parallel()

	type runningServer struct {
		framework.RunningServer
		virtualWorkspaceClientContexts []helpers.VirtualWorkspaceClientContext
		virtualWorkspaceClients        []kubernetes.Interface
		virtualWorkspaces              virtualWorkspacesDescription
	}
	var testCases = []struct {
		name                           string
		virtualWorkspaces              virtualWorkspacesDescription
		virtualWorkspaceClientContexts []helpers.VirtualWorkspaceClientContext
		work                           func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "Test that discovery is correctly published at the right subpaths for each APIGroup-based virtual workspace",
			virtualWorkspaceClientContexts: []helpers.VirtualWorkspaceClientContext{
				{
					User:   framework.LoopbackUser,
					Prefix: "/vw1",
				},
				{
					User:   framework.LoopbackUser,
					Prefix: "/vw2",
				},
			},
			virtualWorkspaces: []virtualWorkspaceDescription{
				{
					prefix: "vw1",
					groups: []virtualWorkspaceAPIGroupDescription{
						{
							gv: schema.GroupVersion{Group: "vw1group1", Version: "v1"},
							resources: []func() virtualWorkspaceResourceDescription{
								func() virtualWorkspaceResourceDescription {
									type Vw1Group1Resource1 struct{ unstructured.Unstructured }
									return virtualWorkspaceResourceDescription{
										namespaceScoped:      false,
										allowCreateAndDelete: false,
										new:                  func() runtime.Unstructured { return &Vw1Group1Resource1{} },
									}
								},
								func() virtualWorkspaceResourceDescription {
									type Vw1Group1Resource2 struct{ unstructured.Unstructured }
									return virtualWorkspaceResourceDescription{
										namespaceScoped:      true,
										allowCreateAndDelete: true,
										new:                  func() runtime.Unstructured { return &Vw1Group1Resource2{} },
									}
								},
							},
						},
						{
							gv: schema.GroupVersion{Group: "vw1group2", Version: "v1"},
							resources: []func() virtualWorkspaceResourceDescription{
								func() virtualWorkspaceResourceDescription {
									type Vw1Group2Resource1 struct{ unstructured.Unstructured }
									return virtualWorkspaceResourceDescription{
										namespaceScoped:      false,
										allowCreateAndDelete: false,
										new:                  func() runtime.Unstructured { return &Vw1Group2Resource1{} },
									}
								},
							},
						},
					},
				},
				{
					prefix: "vw2",
					groups: []virtualWorkspaceAPIGroupDescription{
						{
							gv: schema.GroupVersion{Group: "vw2group1", Version: "v1"},
							resources: []func() virtualWorkspaceResourceDescription{
								func() virtualWorkspaceResourceDescription {
									type Vw2Group1Resource1 struct{ unstructured.Unstructured }
									return virtualWorkspaceResourceDescription{
										namespaceScoped:      true,
										allowCreateAndDelete: false,
										new:                  func() runtime.Unstructured { return &Vw2Group1Resource1{} },
									}
								},
							},
						},
					},
				},
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				vw1Client := server.virtualWorkspaceClients[0]

				_, vw1ServerResources, err := vw1Client.Discovery().ServerGroupsAndResources()
				require.NoError(t, err)
				require.Equal(t,
					[]*metav1.APIResourceList{
						{
							TypeMeta: metav1.TypeMeta{
								Kind:       "APIResourceList",
								APIVersion: "v1",
							},
							GroupVersion: "vw1group1/v1",
							APIResources: []metav1.APIResource{
								{
									Kind:       "Vw1Group1Resource1",
									Name:       "vw1group1resource1",
									Namespaced: false,
									Group:      "vw1group1",
									Version:    "v1",
									Verbs:      metav1.Verbs{"get", "list"},
								},
								{
									Kind:       "Vw1Group1Resource2",
									Name:       "vw1group1resource2",
									Namespaced: true,
									Group:      "vw1group1",
									Version:    "v1",
									Verbs:      metav1.Verbs{"create", "delete", "get", "list"},
								},
							},
						},
						{
							TypeMeta: metav1.TypeMeta{
								Kind:       "APIResourceList",
								APIVersion: "v1",
							},
							GroupVersion: "vw1group2/v1",
							APIResources: []metav1.APIResource{
								{
									Kind:       "Vw1Group2Resource1",
									Name:       "vw1group2resource1",
									Namespaced: false,
									Group:      "vw1group2",
									Version:    "v1",
									Verbs:      metav1.Verbs{"get", "list"},
								},
							},
						},
					},
					sortAPIResourceList(vw1ServerResources),
				)

				vw2Client := server.virtualWorkspaceClients[1]
				_, vw2ServerResources, err := vw2Client.Discovery().ServerGroupsAndResources()
				require.NoError(t, err)
				require.Equal(t,
					[]*metav1.APIResourceList{
						{
							TypeMeta: metav1.TypeMeta{
								Kind:       "APIResourceList",
								APIVersion: "v1",
							},
							GroupVersion: "vw2group1/v1",
							APIResources: []metav1.APIResource{
								{
									Kind:       "Vw2Group1Resource1",
									Name:       "vw2group1resource1",
									Namespaced: true,
									Group:      "vw2group1",
									Version:    "v1",
									Verbs:      metav1.Verbs{"get", "list"},
								},
							},
						},
					},
					sortAPIResourceList(vw2ServerResources),
				)
			},
		},
		{
			name: "Test that OpenAPI/v2 is correctly published at the right subpaths for each APIGroup-based virtual workspace",
			virtualWorkspaceClientContexts: []helpers.VirtualWorkspaceClientContext{
				{
					User:   framework.LoopbackUser,
					Prefix: "/vw1",
				},
				{
					User:   framework.LoopbackUser,
					Prefix: "/vw2",
				},
			},
			virtualWorkspaces: []virtualWorkspaceDescription{
				{
					prefix: "vw1",
					groups: []virtualWorkspaceAPIGroupDescription{
						{
							gv: schema.GroupVersion{Group: "vw1group1", Version: "v1"},
							resources: []func() virtualWorkspaceResourceDescription{
								func() virtualWorkspaceResourceDescription {
									type Vw1Group1Resource1 struct{ unstructured.Unstructured }
									return virtualWorkspaceResourceDescription{
										namespaceScoped:      false,
										allowCreateAndDelete: false,
										new:                  func() runtime.Unstructured { return &Vw1Group1Resource1{} },
									}
								},
								func() virtualWorkspaceResourceDescription {
									type Vw1Group1Resource2 struct{ unstructured.Unstructured }
									return virtualWorkspaceResourceDescription{
										namespaceScoped:      true,
										allowCreateAndDelete: true,
										new:                  func() runtime.Unstructured { return &Vw1Group1Resource2{} },
									}
								},
							},
						},
						{
							gv: schema.GroupVersion{Group: "vw1group2", Version: "v1"},
							resources: []func() virtualWorkspaceResourceDescription{
								func() virtualWorkspaceResourceDescription {
									type Vw1Group2Resource1 struct{ unstructured.Unstructured }
									return virtualWorkspaceResourceDescription{
										namespaceScoped:      false,
										allowCreateAndDelete: false,
										new:                  func() runtime.Unstructured { return &Vw1Group2Resource1{} },
									}
								},
							},
						},
					},
				},
				{
					prefix: "vw2",
					groups: []virtualWorkspaceAPIGroupDescription{
						{
							gv: schema.GroupVersion{Group: "vw2group1", Version: "v1"},
							resources: []func() virtualWorkspaceResourceDescription{
								func() virtualWorkspaceResourceDescription {
									type Vw2Group1Resource1 struct{ unstructured.Unstructured }
									return virtualWorkspaceResourceDescription{
										namespaceScoped:      true,
										allowCreateAndDelete: false,
										new:                  func() runtime.Unstructured { return &Vw2Group1Resource1{} },
									}
								},
							},
						},
					},
				},
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				testJson := func(expected, actual *yaml.Node) {
					expectedJsonBytes, err1 := json.Marshal(expected)
					require.NoError(t, err1)

					actualJsonBytes, err2 := json.Marshal(actual)
					require.NoError(t, err2)

					require.JSONEq(t, string(expectedJsonBytes), string(actualJsonBytes))
				}
				namer := openapinamer.NewDefinitionNamer()
				for i := 0; i < 2; i++ {
					vwClient := server.virtualWorkspaceClients[i]
					vw := server.virtualWorkspaces[i]

					vwOpenAPIDocument, err := vwClient.Discovery().OpenAPISchema()
					require.NoError(t, err)

					testJson((&openapi_v2.Info{
						Title:   "KCP Virtual Workspace for " + vw.prefix,
						Version: "unversioned",
					}).ToRawInfo(), vwOpenAPIDocument.GetInfo().ToRawInfo())
					paths := map[string]*openapi_v2.PathItem{}
					for _, path := range vwOpenAPIDocument.GetPaths().Path {
						paths[path.GetName()] = path.GetValue()
					}
					definitions := map[string]*openapi_v2.Schema{}
					for _, def := range vwOpenAPIDocument.GetDefinitions().AdditionalProperties {
						definitions[def.GetName()] = def.Value
					}
					for _, group := range vw.groups {
						for _, resource := range group.resources {
							resource := resource()
							openAPITypeName := util.GetCanonicalTypeName(resource.new())
							openAPIDefinitionName, _ := namer.GetDefinitionName(openAPITypeName)
							if assert.Contains(t, definitions, openAPIDefinitionName, "OpenAPI definitions should contain definition %s for type %s", openAPIDefinitionName, openAPITypeName) {
								require.Equal(t, resource.OpenAPISchemaDescription(), definitions[openAPIDefinitionName].Description, "OpenAPI Schema description is not correct for %s", openAPIDefinitionName)
							}
							separatorPlaceholder := "/"
							if resource.namespaceScoped {
								separatorPlaceholder = "/namespaces/{namespace}/"
							}
							pathPrefix := "/apis/" + group.gv.Group + "/" + group.gv.Version + separatorPlaceholder + resource.ResourceName() + "/{name}"

							require.Contains(t, paths, pathPrefix, "OpenAPI paths should contain path %s for resource %s", pathPrefix, resource.ResourceName())
						}
					}

				}
			},
		},
	}

	const serverName = "main"

	f := framework.NewKcpFixture(t,
		framework.KcpConfig{
			Name: serverName,
			Args: []string{
				"--run-controllers=false",
			},
			RunInProcess: true,
		},
	)

	for i := range testCases {
		testCase := testCases[i]

		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, 1, len(f.Servers), "incorrect number of servers")
			server := f.Servers[serverName]

			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				withDeadline, cancel := context.WithDeadline(ctx, deadline)
				t.Cleanup(cancel)
				ctx = withDeadline
			}

			vw := helpers.VirtualWorkspace{
				BuildSubCommandOptions: func(kcpServer framework.RunningServer) virtualcmd.SubCommandOptions {
					return &compositecmd.CompositeSubCommandOptions{
						StoragesPerPrefix:              testCase.virtualWorkspaces.ToRestStorages(),
						GetOpenAPIDefinitionsPerPrefix: testCase.virtualWorkspaces.GetOpenAPIDefinitions(),
					}
				},
				ClientContexts: testCase.virtualWorkspaceClientContexts,
			}

			vwConfigs, err := vw.Setup(t, ctx, server, "kljdslfkjaslkdjflasdjflkasdjflk")
			require.NoError(t, err)

			virtualWorkspaceClients := []kubernetes.Interface{}
			for _, vwConfig := range vwConfigs {
				kubeClient, err := kubernetes.NewForConfig(vwConfig)
				require.NoError(t, err, "failed to construct client for server")
				virtualWorkspaceClients = append(virtualWorkspaceClients, kubeClient)
			}

			testCase.work(ctx, t, runningServer{
				RunningServer:                  server,
				virtualWorkspaceClientContexts: testCase.virtualWorkspaceClientContexts,
				virtualWorkspaceClients:        virtualWorkspaceClients,
				virtualWorkspaces:              testCase.virtualWorkspaces,
			})
		})
	}
}

type ByGroupVersion []*metav1.APIResourceList

func (a ByGroupVersion) Len() int           { return len(a) }
func (a ByGroupVersion) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByGroupVersion) Less(i, j int) bool { return a[i].GroupVersion < a[j].GroupVersion }

type ByName []metav1.APIResource

func (a ByName) Len() int           { return len(a) }
func (a ByName) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByName) Less(i, j int) bool { return a[i].Name < a[j].Name }

func sortAPIResourceList(list []*metav1.APIResourceList) []*metav1.APIResourceList {
	sort.Sort(ByGroupVersion(list))
	for _, resource := range list {
		sort.Sort(ByName(resource.APIResources))
	}
	return list
}
*/
