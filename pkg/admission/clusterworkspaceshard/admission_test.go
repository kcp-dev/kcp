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

package clusterworkspaceshard

import (
	"context"
	"testing"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/require"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/diff"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func createAttr(ws *tenancyv1alpha1.ClusterWorkspaceShard) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(ws),
		nil,
		tenancyv1alpha1.Kind("ClusterWorkspaceShard").WithVersion("v1alpha1"),
		"",
		ws.Name,
		tenancyv1alpha1.Resource("clusterworkspaceshards").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func updateAttr(ws, old *tenancyv1alpha1.ClusterWorkspaceShard) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(ws),
		helpers.ToUnstructuredOrDie(old),
		tenancyv1alpha1.Kind("ClusterWorkspace").WithVersion("v1alpha1"),
		"",
		ws.Name,
		tenancyv1alpha1.Resource("clusterworkspaceshards").WithVersion("v1alpha1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func TestAdmit(t *testing.T) {
	tests := []struct {
		name                      string
		a                         admission.Attributes
		emptyExternalAddress      bool
		noExternalAddressProvider bool
		expectedObj               runtime.Object
		wantErr                   bool
	}{
		{
			name: "does nothing on update when baseURL and externalURL are set",
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL:     "https://boston2.kcp.dev",
					ExternalURL: "https://kcp2.dev",
				},
			},
				&tenancyv1alpha1.ClusterWorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						BaseURL:     "https://boston.kcp.dev",
						ExternalURL: "https://kcp.dev",
					},
				}),
			expectedObj: &tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL:     "https://boston2.kcp.dev",
					ExternalURL: "https://kcp2.dev",
				},
			},
		},
		{
			name: "does nothing on create when baseURL and externalURL are set",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL:     "https://boston2.kcp.dev",
					ExternalURL: "https://kcp2.dev",
				},
			}),
			expectedObj: &tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL:     "https://boston2.kcp.dev",
					ExternalURL: "https://kcp2.dev",
				},
			},
		},
		{
			name: "default externalURL on update when baseURL is set",
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL: "https://boston2.kcp.dev",
				},
			},
				&tenancyv1alpha1.ClusterWorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						BaseURL:     "https://boston2.kcp.dev",
						ExternalURL: "https://kcp.dev",
					},
				}),
			expectedObj: &tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL:     "https://boston2.kcp.dev",
					ExternalURL: "https://boston2.kcp.dev",
				},
			},
		},
		{
			name: "default externalURL on create when baseURL is set",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL: "https://boston2.kcp.dev",
				},
			}),
			expectedObj: &tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL:     "https://boston2.kcp.dev",
					ExternalURL: "https://boston2.kcp.dev",
				},
			},
		},
		{
			name: "default baseURL on update",
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					ExternalURL: "https://kcp.dev",
				},
			},
				&tenancyv1alpha1.ClusterWorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						BaseURL:     "https://boston.kcp.dev",
						ExternalURL: "https://kcp.dev",
					},
				}),
			expectedObj: &tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL:     "https://external.kcp.dev",
					ExternalURL: "https://kcp.dev",
				},
			},
		},
		{
			name: "default baseURL on create",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					ExternalURL: "https://kcp.dev",
				},
			}),
			expectedObj: &tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL:     "https://external.kcp.dev",
					ExternalURL: "https://kcp.dev",
				},
			},
		},
		{
			name: "default externalURL and baseURL on update",
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{},
			},
				&tenancyv1alpha1.ClusterWorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						BaseURL:     "https://boston.kcp.dev",
						ExternalURL: "https://kcp.dev",
					},
				}),
			expectedObj: &tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL:     "https://external.kcp.dev",
					ExternalURL: "https://external.kcp.dev",
				},
			},
		},
		{
			name: "default baseURL on create",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{},
			}),
			expectedObj: &tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL:     "https://external.kcp.dev",
					ExternalURL: "https://external.kcp.dev",
				},
			},
		},
		{
			name: "ignores different resources",
			a: admission.NewAttributesRecord(
				&unstructured.Unstructured{Object: map[string]interface{}{
					"apiVersion": tenancyv1alpha1.SchemeGroupVersion.String(),
					"kind":       "ClusterWorkspace",
					"metadata": map[string]interface{}{
						"name":              "test",
						"creationTimestamp": nil,
					},
					"spec": map[string]interface{}{},
					"status": map[string]interface{}{
						"location": map[string]interface{}{},
					},
				}},
				nil,
				tenancyv1alpha1.Kind("ClusterWorkspace").WithVersion("v1alpha1"),
				"",
				"test",
				tenancyv1alpha1.Resource("clusterworkspaces").WithVersion("v1alpha1"),
				"",
				admission.Create,
				&metav1.CreateOptions{},
				false,
				&user.DefaultInfo{},
			),
			expectedObj: &tenancyv1alpha1.ClusterWorkspace{
				TypeMeta: metav1.TypeMeta{
					APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(),
					Kind:       "ClusterWorkspace",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			},
		},
		{
			name: "default externalURL on create when baseURL is set",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL: "https://boston2.kcp.dev",
				},
			}),
			noExternalAddressProvider: true,
			expectedObj: &tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL:     "https://boston2.kcp.dev",
					ExternalURL: "https://boston2.kcp.dev",
				},
			},
		},
		{
			name: "fails on create when baseURL is not set and external address provider is nil",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{},
			}),
			noExternalAddressProvider: true,
			wantErr:                   true,
		},
		{
			name: "fails on update when baseURL is not set and external address provider is nil",
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{},
			},
				&tenancyv1alpha1.ClusterWorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{},
				}),
			noExternalAddressProvider: true,
			wantErr:                   true,
		},
		{
			name: "fails on create when baseURL is not set and external address provider returns empty string",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{},
			}),
			emptyExternalAddress: true,
			wantErr:              true,
		},
		{
			name: "fails on update when baseURL is not set and external address provider returns empty string",
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{},
			},
				&tenancyv1alpha1.ClusterWorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{},
				}),
			emptyExternalAddress: true,
			wantErr:              true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &clusterWorkspaceShard{
				Handler:                 admission.NewHandler(admission.Create, admission.Update),
				externalAddressProvider: func() string { return "external.kcp.dev" },
			}
			if tt.noExternalAddressProvider {
				o.externalAddressProvider = nil
			} else if tt.emptyExternalAddress {
				o.externalAddressProvider = func() string {
					return ""
				}
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root:org")})
			if err := o.Admit(ctx, tt.a, nil); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			} else if err == nil {
				got, ok := tt.a.GetObject().(*unstructured.Unstructured)
				require.True(t, ok, "expected unstructured, got %T", tt.a.GetObject())
				expected := helpers.ToUnstructuredOrDie(tt.expectedObj)
				if !apiequality.Semantic.DeepEqual(expected, got) {
					t.Fatalf("unexpected result (A expected, B got): %s", diff.ObjectDiff(expected, got))
				}
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		a       admission.Attributes
		wantErr bool
	}{
		{
			name: "accept non-empty baseURL and externalURL on update",
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL:     "https://kcp",
					ExternalURL: "https://kcp",
				},
			},
				&tenancyv1alpha1.ClusterWorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						BaseURL:     "https://kcp",
						ExternalURL: "https://kcp",
					},
				}),
		},
		{
			name: "accept non-empty baseURL and externalURL on create",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL:     "https://kcp",
					ExternalURL: "https://kcp",
				},
			}),
		},
		{
			name: "reject empty baseURL on update",
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					ExternalURL: "https://kcp",
				},
			},
				&tenancyv1alpha1.ClusterWorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						BaseURL:     "https://kcp",
						ExternalURL: "https://kcp",
					},
				}),
			wantErr: true,
		},
		{
			name: "reject empty baseURL on create",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					ExternalURL: "https://kcp",
				},
			}),
			wantErr: true,
		},
		{
			name: "reject empty externalURL on update",
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL: "https://kcp",
				},
			},
				&tenancyv1alpha1.ClusterWorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
						BaseURL:     "https://kcp",
						ExternalURL: "https://kcp",
					},
				}),
			wantErr: true,
		},
		{
			name: "reject empty externalURL on create",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
					BaseURL: "https://kcp",
				},
			}),
			wantErr: true,
		},
		{
			name: "ignores different resources",
			a: admission.NewAttributesRecord(
				&tenancyv1alpha1.ClusterWorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
				},
				nil,
				tenancyv1alpha1.Kind("ClusterWorkspace").WithVersion("v1alpha1"),
				"",
				"test",
				tenancyv1alpha1.Resource("clusterworkspaces").WithVersion("v1alpha1"),
				"",
				admission.Create,
				&metav1.CreateOptions{},
				false,
				&user.DefaultInfo{},
			),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &clusterWorkspaceShard{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root")})
			if err := o.Validate(ctx, tt.a, nil); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
