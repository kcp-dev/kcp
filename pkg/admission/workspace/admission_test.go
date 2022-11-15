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

package workspace

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
)

func createAttr(ws *tenancyv1beta1.Workspace) admission.Attributes {
	return createAttrWithUser(ws, &user.DefaultInfo{})
}

func createAttrWithUser(ws *tenancyv1beta1.Workspace, info user.Info) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(ws),
		nil,
		tenancyv1beta1.Kind("Workspace").WithVersion("v1beta1"),
		"",
		ws.Name,
		tenancyv1beta1.Resource("workspaces").WithVersion("v1beta1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		info,
	)
}

func updateAttr(ws, old *tenancyv1beta1.Workspace) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(ws),
		helpers.ToUnstructuredOrDie(old),
		tenancyv1beta1.Kind("Workspace").WithVersion("v1beta1"),
		"",
		ws.Name,
		tenancyv1beta1.Resource("workspaces").WithVersion("v1beta1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func TestAdmit(t *testing.T) {
	tests := []struct {
		name        string
		types       []*tenancyv1alpha1.ClusterWorkspaceType
		clusterName logicalcluster.Name
		a           admission.Attributes
		expectedObj runtime.Object
		wantErr     bool
	}{
		{
			name: "adds user information on create",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:foo").ClusterWorkspaceType,
			},
			clusterName: logicalcluster.New("root:org:ws"),
			a: createAttrWithUser(&tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
			}, &user.DefaultInfo{
				Name:   "someone",
				UID:    "id",
				Groups: []string{"a", "b"},
				Extra: map[string][]string{
					"one": {"1", "01"},
				},
			}),
			expectedObj: &tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.dev/owner": `{"username":"someone","uid":"id","groups":["a","b"],"extra":{"one":["1","01"]}}`,
					},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
			},
		},
		{
			name: "keep user information on create when system:masters",
			a: createAttrWithUser(&tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.dev/owner": `{"username":"someoneelse","uid":"otherid","groups":["c","d"],"extra":{"two":["2","02"]}}`,
					},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}, &user.DefaultInfo{
				Name:   "someone",
				UID:    "id",
				Groups: []string{"a", "b", "system:masters"},
				Extra: map[string][]string{
					"one": {"1", "01"},
				},
			}),
			expectedObj: &tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.dev/owner": `{"username":"someoneelse","uid":"otherid","groups":["c","d"],"extra":{"two":["2","02"]}}`,
					},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			},
		},
		{
			name: "override user information on create when not system:masters",
			a: createAttrWithUser(&tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.dev/owner": `{"username":"someoneelse","uid":"otherid","groups":["c","d"],"extra":{"two":["2","02"]}}`,
					},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}, &user.DefaultInfo{
				Name:   "someone",
				UID:    "id",
				Groups: []string{"a", "b"},
				Extra: map[string][]string{
					"one": {"1", "01"},
				},
			}),
			expectedObj: &tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.dev/owner": `{"username":"someone","uid":"id","groups":["a","b"],"extra":{"one":["1","01"]}}`,
					},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &workspace{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: tt.clusterName})
			if err := o.Admit(ctx, tt.a, nil); (err != nil) != tt.wantErr {
				t.Fatalf("Admit() error = %v, wantErr %v", err, tt.wantErr)
			} else if err == nil {
				got, ok := tt.a.GetObject().(*unstructured.Unstructured)
				require.True(t, ok, "expected unstructured, got %T", tt.a.GetObject())
				expected := helpers.ToUnstructuredOrDie(tt.expectedObj)
				if diff := cmp.Diff(expected, got); diff != "" {
					t.Fatalf("got incorrect result: %v", diff)
				}
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name           string
		a              admission.Attributes
		expectedErrors []string
	}{
		{
			name: "rejects type mutations",
			a: updateAttr(&tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
			},
				&tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test",
						Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
					},
					Spec: tenancyv1beta1.WorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "universal",
							Path: "root:org",
						},
					},
				}),
			expectedErrors: []string{"field is immutable"},
		},
		{
			name: "rejects unsetting cluster",
			a: updateAttr(&tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1beta1.WorkspaceStatus{}},
				&tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test",
						Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
					},
					Spec: tenancyv1beta1.WorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "foo",
							Path: "root:org",
						},
					},
					Status: tenancyv1beta1.WorkspaceStatus{
						Cluster: "somewhere",
					},
				}),
			expectedErrors: []string{"status.cluster cannot be unset"},
		},
		{
			name: "rejects transition from Initializing with non-empty initializers",
			a: updateAttr(&tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1beta1.WorkspaceStatus{
					Phase:        tenancyv1alpha1.WorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{"a"},
					Cluster:      "somewhere",
					URL:          "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
				&tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test",
						Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
					},
					Spec: tenancyv1beta1.WorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "foo",
							Path: "root:org",
						},
					},
					Status: tenancyv1beta1.WorkspaceStatus{
						Phase:        tenancyv1alpha1.WorkspacePhaseInitializing,
						Initializers: []tenancyv1alpha1.WorkspaceInitializer{"a"},
					},
				}),
			expectedErrors: []string{"spec.initializers must be empty for phase Ready"},
		},
		{
			name: "allows transition from Initializing with empty initializers",
			a: updateAttr(&tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1beta1.WorkspaceStatus{
					Phase:        tenancyv1alpha1.WorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{},
					Cluster:      "somewhere",
					URL:          "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
				&tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test",
						Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
					},
					Spec: tenancyv1beta1.WorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "foo",
							Path: "root:org",
						},
					},
					Status: tenancyv1beta1.WorkspaceStatus{
						Phase:        tenancyv1alpha1.WorkspacePhaseInitializing,
						Initializers: []tenancyv1alpha1.WorkspaceInitializer{"a"},
						Cluster:      "somewhere",
						URL:          "https://kcp.bigcorp.com/clusters/org:test",
					},
				}),
		},
		{
			name: "allows transition to ready directly when valid",
			a: updateAttr(&tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1beta1.WorkspaceStatus{
					Phase:        tenancyv1alpha1.WorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{},
					Cluster:      "somewhere",
					URL:          "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
				&tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test",
						Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
					},
					Spec: tenancyv1beta1.WorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "foo",
							Path: "root:org",
						},
					},
					Status: tenancyv1beta1.WorkspaceStatus{
						Phase:        tenancyv1alpha1.WorkspacePhaseScheduling,
						Initializers: []tenancyv1alpha1.WorkspaceInitializer{"a"},
					},
				}),
		},
		{
			name: "allows creation to ready directly when valid",
			a: createAttr(&tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1beta1.WorkspaceStatus{
					Phase:        tenancyv1alpha1.WorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{},
					Cluster:      "somewhere",
					URL:          "https://kcp.bigcorp.com/clusters/org:test",
				},
			}),
		},
		{
			name: "rejects transition to ready directly when invalid",
			a: updateAttr(&tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1beta1.WorkspaceStatus{
					Phase:        tenancyv1alpha1.WorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{},
					Cluster:      "somewhere",
				},
			},
				&tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test",
						Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
					},
					Spec: tenancyv1beta1.WorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "foo",
							Path: "root:org",
						},
					},
					Status: tenancyv1beta1.WorkspaceStatus{
						Phase:        tenancyv1alpha1.WorkspacePhaseScheduling,
						Initializers: []tenancyv1alpha1.WorkspaceInitializer{"a"},
					},
				}),
			expectedErrors: []string{"status.URL must be set for phase Ready"},
		},
		{
			name: "rejects transition to previous phase",
			a: updateAttr(&tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1beta1.WorkspaceStatus{
					Phase:        tenancyv1alpha1.WorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{},
					Cluster:      "somewhere",
					URL:          "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
				&tenancyv1beta1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test",
						Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
					},
					Spec: tenancyv1beta1.WorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "foo",
							Path: "root:org",
						},
					},
					Status: tenancyv1beta1.WorkspaceStatus{
						Phase:        tenancyv1alpha1.WorkspacePhaseReady,
						Initializers: []tenancyv1alpha1.WorkspaceInitializer{},
						Cluster:      "somewhere",
						URL:          "https://kcp.bigcorp.com/clusters/org:test",
					},
				}),
			expectedErrors: []string{"cannot transition from \"Ready\" to \"Initializing\""},
		},
		{
			name: "ignores different resources",
			a: admission.NewAttributesRecord(
				&tenancyv1alpha1.ClusterWorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test",
						Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
					},
				},
				nil,
				tenancyv1alpha1.Kind("ClusterWorkspaceShard").WithVersion("v1alpha1"),
				"",
				"test",
				tenancyv1alpha1.Resource("clusterworkspaceshards").WithVersion("v1alpha1"),
				"",
				admission.Create,
				&metav1.CreateOptions{},
				false,
				&user.DefaultInfo{},
			),
		},
		{
			name: "checks user information on create",
			a: createAttrWithUser(&tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.dev/owner": "{}"},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
			}, &user.DefaultInfo{
				Name:   "someone",
				UID:    "id",
				Groups: []string{"a", "b"},
				Extra: map[string][]string{
					"one": {"1", "01"},
				},
			}),
			expectedErrors: []string{"expected user annotation experimental.tenancy.kcp.dev/owner={\"username\":\"someone\",\"uid\":\"id\",\"groups\":[\"a\",\"b\"],\"extra\":{\"one\":[\"1\",\"01\"]}}"},
		},
		{
			name: "accept user information on create when system:masters",
			a: createAttrWithUser(&tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.dev/owner": `{"username":"someoneelse","uid":"otherid","groups":["c","d"],"extra":{"two":["2","02"]}}`,
					},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}, &user.DefaultInfo{
				Name:   "someone",
				UID:    "id",
				Groups: []string{"a", "b", "system:masters"},
				Extra: map[string][]string{
					"one": {"1", "01"},
				},
			}),
		},
		{
			name: "reject wrong user information on create when not system:masters",
			a: createAttrWithUser(&tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.dev/owner": `{"username":"someoneelse","uid":"otherid","groups":["c","d"],"extra":{"two":["2","02"]}}`,
					},
				},
				Spec: tenancyv1beta1.WorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}, &user.DefaultInfo{
				Name:   "someone",
				UID:    "id",
				Groups: []string{"a", "b"},
				Extra: map[string][]string{
					"one": {"1", "01"},
				},
			}),
			expectedErrors: []string{"expected user annotation experimental.tenancy.kcp.dev/owner={\"username\":\"someone\",\"uid\":\"id\",\"groups\":[\"a\",\"b\"],\"extra\":{\"one\":[\"1\",\"01\"]}}"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &workspace{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root:org")})
			err := o.Validate(ctx, tt.a, nil)
			t.Logf("%v", err)
			wantErr := len(tt.expectedErrors) > 0
			require.Equal(t, wantErr, err != nil)

			if err != nil {
				t.Logf("Got admission errors: %v", err)
				for _, expected := range tt.expectedErrors {
					require.Contains(t, err.Error(), expected)
				}
			}
		})
	}
}

type builder struct {
	*tenancyv1alpha1.ClusterWorkspaceType
}

func newType(qualifiedName string) builder {
	path, name := logicalcluster.New(qualifiedName).Split()
	return builder{ClusterWorkspaceType: &tenancyv1alpha1.ClusterWorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: path.String(),
			},
		},
	}}
}
