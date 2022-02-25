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

package clusterworkspacetypeexists

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/utils/diff"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func createAttr(ws *tenancyv1alpha1.ClusterWorkspace) admission.Attributes {
	return admission.NewAttributesRecord(
		ws,
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
	)
}

func updateAttr(ws, old *tenancyv1alpha1.ClusterWorkspace) admission.Attributes {
	return admission.NewAttributesRecord(
		ws,
		old,
		tenancyv1alpha1.Kind("ClusterWorkspace").WithVersion("v1alpha1"),
		"",
		"test",
		tenancyv1alpha1.Resource("clusterworkspaces").WithVersion("v1alpha1"),
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
		a           admission.Attributes
		expectedObj runtime.Object
		wantErr     bool
	}{
		{
			"adds initializers during transition to initializing",
			[]*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#foo",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"a", "b"},
					},
				},
			},
			updateAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"a", "b"},
					Location:     tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:      "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: "Foo",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase:        tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					},
				}),
			&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"a", "b"},
					Location:     tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:      "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
			false,
		},
		{
			"does not add initializers during transition not to initializing",
			[]*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#foo",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"a", "b"},
					},
				},
			},
			updateAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					Location:     tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:      "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: "Foo",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase:        tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					},
				}),
			&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					Location:     tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:      "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
			false,
		},
		{
			"does nothing for universal type",
			nil,
			updateAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Universal",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					Location:     tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:      "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: "Universal",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase:        tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					},
				}),
			&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Universal",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					Location:     tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:      "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
			false,
		},
		{
			"ignors different resources",
			nil,
			admission.NewAttributesRecord(
				&tenancyv1alpha1.WorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
				},
				nil,
				tenancyv1alpha1.Kind("WorkspaceShard").WithVersion("v1alpha1"),
				"",
				"test",
				tenancyv1alpha1.Resource("workspaceshards").WithVersion("v1alpha1"),
				"",
				admission.Create,
				&metav1.CreateOptions{},
				false,
				&user.DefaultInfo{},
			),
			&tenancyv1alpha1.WorkspaceShard{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &clusterWorkspaceTypeExists{
				Handler:    admission.NewHandler(admission.Create, admission.Update),
				typeLister: fakeClusterWorkspaceTypeLister(tt.types),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: "root:org"})
			if err := o.Admit(ctx, tt.a, nil); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			} else if !reflect.DeepEqual(tt.expectedObj, tt.a.GetObject()) {
				t.Errorf("unexpected result (A expected, B got): %s", diff.ObjectReflectDiff(tt.expectedObj, tt.a.GetObject()))
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		types   []*tenancyv1alpha1.ClusterWorkspaceType
		a       admission.Attributes
		wantErr bool
	}{
		{
			"passes create if type exists",
			[]*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#foo",
					},
				},
			},
			createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
			}),
			false,
		},
		{
			"fails if type does not exists",
			nil,
			createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
			}),
			true,
		},
		{
			"fails if type only exists in different workspace",
			[]*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:bigcorp#$#foo",
					},
				},
			},
			createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
			}),
			true,
		},
		{
			"Universal always exists implicitly",
			nil,
			createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Universal",
				},
			}),
			false,
		},
		{
			"Universal works too when it exists",
			[]*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#universal",
					},
				},
			},
			createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Universal",
				},
			}),
			false,
		},
		{
			"rejects type mutations",
			[]*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#foo",
					},
				},
			},
			updateAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
			},
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: "Universal",
					},
				}),
			true,
		},
		{
			"validates initializers on phase transition",
			[]*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#foo",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"a", "b"},
					},
				},
			},
			updateAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"a"}, // b missing
				},
			},
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: "Foo",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase: tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
					},
				}),
			true,
		},
		{
			"passes with all initializers or more on phase transition",
			[]*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#foo",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"a", "b"},
					},
				},
			},
			updateAttr(
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: "Foo",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"a", "b", "c"},
						Location:     tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
						BaseURL:      "https://kcp.bigcorp.com/clusters/org:test",
					},
				},
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: "Foo",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase: tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
					},
				}),
			false,
		},
		{
			"rejects transition from Initializing with non-empty initializers",
			[]*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#foo",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"a", "b"},
					},
				},
			},
			updateAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"a"},
				},
			},
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: "Foo",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"a"},
					},
				}),
			true,
		},
		{
			"allows transition from Initializing with empty initializers",
			[]*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#foo",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"a", "b"},
					},
				},
			},
			updateAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					Location:     tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:      "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: "Foo",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"a"},
						Location:     tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
						BaseURL:      "https://kcp.bigcorp.com/clusters/org:test",
					},
				}),
			false,
		},
		{
			"ignors different resources",
			nil,
			admission.NewAttributesRecord(
				&tenancyv1alpha1.WorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
				},
				nil,
				tenancyv1alpha1.Kind("WorkspaceShard").WithVersion("v1alpha1"),
				"",
				"test",
				tenancyv1alpha1.Resource("workspaceshards").WithVersion("v1alpha1"),
				"",
				admission.Create,
				&metav1.CreateOptions{},
				false,
				&user.DefaultInfo{},
			),
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &clusterWorkspaceTypeExists{
				Handler:    admission.NewHandler(admission.Create, admission.Update),
				typeLister: fakeClusterWorkspaceTypeLister(tt.types),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: "root:org"})
			if err := o.Validate(ctx, tt.a, nil); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

type fakeClusterWorkspaceTypeLister []*tenancyv1alpha1.ClusterWorkspaceType

func (l fakeClusterWorkspaceTypeLister) List(selector labels.Selector) (ret []*tenancyv1alpha1.ClusterWorkspaceType, err error) {
	return l.ListWithContext(context.Background(), selector)
}

func (l fakeClusterWorkspaceTypeLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*tenancyv1alpha1.ClusterWorkspaceType, err error) {
	return l, nil
}

func (l fakeClusterWorkspaceTypeLister) Get(name string) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
	return l.GetWithContext(context.Background(), name)
}

func (l fakeClusterWorkspaceTypeLister) GetWithContext(ctx context.Context, name string) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
	for _, t := range l {
		if t.Name == name {
			return t, nil
		}
	}
	return nil, errors.NewNotFound(tenancyv1alpha1.Resource("clusterworkspacetype"), name)
}
