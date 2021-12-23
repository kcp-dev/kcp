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

package generic

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/kubernetes"

	virtualcmd "github.com/kcp-dev/kcp/pkg/virtual/generic/cmd"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/virtual/generic/compositecmd"
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

type virtualWorkspaceDescription struct {
	prefix string
	groups []virtualWorkspaceAPIGroupDescription
}

func (d virtualWorkspaceDescription) ToRestStorages() map[schema.GroupVersion]map[string]rest.Storage {
	storages := make(map[schema.GroupVersion]map[string]rest.Storage)
	for _, group := range d.groups {
		storages[group.gv] = group.ToRestStorages()
	}
	return storages
}

type virtualWorkspaceAPIGroupDescription struct {
	gv        schema.GroupVersion
	resources []virtualWorkspaceResourceDescription
}
type virtualWorkspaceResourceDescription struct {
	resourceName         string
	kind                 string
	namespaceScoped      bool
	allowCreateAndDelete bool
}

func (d virtualWorkspaceAPIGroupDescription) ToRestStorages() map[string]rest.Storage {
	storages := make(map[string]rest.Storage)
	for _, resource := range d.resources {
		if resource.allowCreateAndDelete {
			type readWriteRest struct {
				*compositecmd.BasicStorage
				*compositecmd.Creater
				*compositecmd.Deleter
			}
			storages[resource.resourceName] = &readWriteRest{
				BasicStorage: &compositecmd.BasicStorage{GVK: d.gv.WithKind(resource.kind), IsNamespaceScoped: resource.namespaceScoped},
			}
		} else {
			storages[resource.resourceName] = &compositecmd.BasicStorage{GVK: d.gv.WithKind(resource.kind), IsNamespaceScoped: resource.namespaceScoped}
		}
	}
	return storages
}

func TestCompositeVirtualWorkspace(t *testing.T) {
	type runningServer struct {
		framework.RunningServer
		virtualWorkspaceClientContexts []helpers.VirtualWorkspaceClientContext
		virtualWorkspaceClients        []kubernetes.Interface
	}
	var testCases = []struct {
		name                           string
		virtualWorkspaces              virtualWorkspacesDescription
		virtualWorkspaceClientContexts []helpers.VirtualWorkspaceClientContext
		work                           func(ctx context.Context, t framework.TestingTInterface, server runningServer)
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
							resources: []virtualWorkspaceResourceDescription{
								{
									resourceName:         "vw1group1resource1",
									kind:                 "Resource1",
									namespaceScoped:      false,
									allowCreateAndDelete: false,
								},
								{
									resourceName:         "vw1group1resource2",
									kind:                 "Resource2",
									namespaceScoped:      true,
									allowCreateAndDelete: true,
								},
							},
						},
						{
							gv: schema.GroupVersion{Group: "vw1group2", Version: "v1"},
							resources: []virtualWorkspaceResourceDescription{
								{
									resourceName:         "vw1group2resource1",
									kind:                 "Resource1",
									namespaceScoped:      false,
									allowCreateAndDelete: false,
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
							resources: []virtualWorkspaceResourceDescription{
								{
									resourceName:         "vw2group1resource1",
									kind:                 "Resource1",
									namespaceScoped:      true,
									allowCreateAndDelete: false,
								},
							},
						},
					},
				},
			},
			work: func(ctx context.Context, t framework.TestingTInterface, server runningServer) {
				vw1Client := server.virtualWorkspaceClients[0]

				_, vw1ServerResources, err := vw1Client.Discovery().ServerGroupsAndResources()
				if err != nil {
					t.Error(err)
				} else {
					assert.Equal(t, []*metav1.APIResourceList{
						{
							TypeMeta: metav1.TypeMeta{
								Kind:       "APIResourceList",
								APIVersion: "v1",
							},
							GroupVersion: "vw1group1/v1",
							APIResources: []metav1.APIResource{
								{
									Kind:       "Resource1",
									Name:       "vw1group1resource1",
									Namespaced: false,
									Group:      "vw1group1",
									Version:    "v1",
									Verbs:      metav1.Verbs{"get", "list"},
								},
								{
									Kind:       "Resource2",
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
									Kind:       "Resource1",
									Name:       "vw1group2resource1",
									Namespaced: false,
									Group:      "vw1group2",
									Version:    "v1",
									Verbs:      metav1.Verbs{"get", "list"},
								},
							},
						},
					}, vw1ServerResources)
				}

				vw2Client := server.virtualWorkspaceClients[1]
				_, vw2ServerResources, err := vw2Client.Discovery().ServerGroupsAndResources()
				if err != nil {
					t.Error(err)
				} else {
					assert.Equal(t, []*metav1.APIResourceList{
						{
							TypeMeta: metav1.TypeMeta{
								Kind:       "APIResourceList",
								APIVersion: "v1",
							},
							GroupVersion: "vw2group1/v1",
							APIResources: []metav1.APIResource{
								{
									Kind:       "Resource1",
									Name:       "vw2group1resource1",
									Namespaced: true,
									Group:      "vw2group1",
									Version:    "v1",
									Verbs:      metav1.Verbs{"get", "list"},
								},
							},
						},
					}, vw2ServerResources)
				}
			},
		},
	}

	const serverName = "main"

	for i := range testCases {
		testCase := testCases[i]

		framework.Run(t, testCase.name, func(t framework.TestingTInterface, servers map[string]framework.RunningServer) {
			if len(servers) != 1 {
				t.Errorf("incorrect number of servers: %d", len(servers))
				return
			}
			server := servers[serverName]

			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				withDeadline, cancel := context.WithDeadline(ctx, deadline)
				t.Cleanup(cancel)
				ctx = withDeadline
			}

			vw := helpers.VirtualWorkspace{
				BuildSubCommandOtions: func(kcpServer framework.RunningServer) virtualcmd.SubCommandOptions {
					return &compositecmd.CompositeSubCommandOptions{
						StoragesPerPrefix: testCase.virtualWorkspaces.ToRestStorages(),
					}
				},
				ClientContexts: testCase.virtualWorkspaceClientContexts,
			}

			vwConfigs, err := vw.Setup(t, ctx, server)
			if err != nil {
				t.Error(err.Error())
				return
			}

			virtualWorkspaceClients := []kubernetes.Interface{}
			for _, vwConfig := range vwConfigs {
				kubeClient, err := kubernetes.NewForConfig(vwConfig)
				if err != nil {
					t.Errorf("failed to construct client for server: %v", err)
					return
				}
				virtualWorkspaceClients = append(virtualWorkspaceClients, kubeClient)
			}

			testCase.work(ctx, t, runningServer{
				RunningServer:                  server,
				virtualWorkspaceClientContexts: testCase.virtualWorkspaceClientContexts,
				virtualWorkspaceClients:        virtualWorkspaceClients,
			})
		}, framework.KcpConfig{
			Name: serverName,
			Args: []string{""},
		})
	}
}
