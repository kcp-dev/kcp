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
	"errors"
	"testing"

	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/diff"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func createAttr(obj *tenancyv1alpha1.ClusterWorkspace) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(obj),
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

func updateAttr(obj, old *tenancyv1alpha1.ClusterWorkspace) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(obj),
		helpers.ToUnstructuredOrDie(old),
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
			name: "adds initializers during transition to initializing",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#foo",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
							{Name: "a", Path: "root:org:ws"},
							{Name: "b", Path: "root:org:ws"},
						},
					},
				},
			},
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase: tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
						{Name: "a", Path: "root:org:ws"},
						{Name: "b", Path: "root:org:ws"},
					},
					Location: tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:  "https://kcp.bigcorp.com/clusters/org:test",
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
			expectedObj: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase: tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
						{Name: "a", Path: "root:org:ws"},
						{Name: "b", Path: "root:org:ws"},
					},
					Location: tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:  "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
		},
		{
			name: "does not add initializers during transition not to initializing",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#foo",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
							{Name: "a", Path: "root:org:ws"},
							{Name: "b", Path: "root:org:ws"},
						},
					},
				},
			},
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspace{
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
			expectedObj: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:    tenancyv1alpha1.ClusterWorkspacePhaseReady,
					Location: tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:  "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
		},
		{
			name: "does nothing for universal type",
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspace{
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
			expectedObj: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Universal",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:    tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Location: tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:  "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
		},
		{
			name: "ignores different resources",
			a: admission.NewAttributesRecord(
				&unstructured.Unstructured{Object: map[string]interface{}{
					"apiVersion": tenancyv1alpha1.SchemeGroupVersion.String(),
					"kind":       "ClusterWorkspaceShard",
					"metadata": map[string]interface{}{
						"name":              "test",
						"creationTimestamp": nil,
					},
					"spec": map[string]interface{}{
						"baseURL":     "",
						"externalURL": "",
					},
					"status": map[string]interface{}{},
				}},
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
			expectedObj: &tenancyv1alpha1.ClusterWorkspaceShard{
				TypeMeta: metav1.TypeMeta{
					APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(),
					Kind:       "ClusterWorkspaceShard",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			},
		},
		{
			name: "adds additional workspace labels if missing",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#foo",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
							{Name: "a", Path: "root:org:ws"},
							{Name: "b", Path: "root:org:ws"},
						},
						AdditionalWorkspaceLabels: map[string]string{
							"new-label":      "default",
							"existing-label": "default",
						},
					},
				},
			},
			a: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Labels: map[string]string{
						"existing-label": "non-default",
					},
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
			}),
			expectedObj: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Labels: map[string]string{
						"new-label":      "default",
						"existing-label": "non-default",
					},
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &clusterWorkspaceTypeExists{
				Handler:    admission.NewHandler(admission.Create, admission.Update),
				typeLister: fakeClusterWorkspaceTypeLister(tt.types),
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
		name  string
		types []*tenancyv1alpha1.ClusterWorkspaceType
		attr  admission.Attributes

		authzDecision authorizer.Decision
		authzError    error

		wantErr bool
	}{
		{
			name: "passes create if type exists",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#foo",
					},
				},
			},
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
			}),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "fails if type does not exists",
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
			}),
			wantErr: true,
		},
		{
			name: "fails if type only exists in different workspace",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:bigcorp#$#foo",
					},
				},
			},
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
			}),
			wantErr: true,
		},
		{
			name: "fails if not allowed",
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
			}),
			authzDecision: authorizer.DecisionNoOpinion,
			wantErr:       true,
		},
		{
			name: "fails if denied",
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
			}),
			authzDecision: authorizer.DecisionDeny,
			wantErr:       true,
		},
		{
			name: "fails if authz error",
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
			}),
			authzError: errors.New("authorizer error"),
			wantErr:    true,
		},
		{
			name: "Universal always exists implicitly if authorized",
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Universal",
				},
			}),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Universal fails if not authorized",
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Universal",
				},
			}),
			authzDecision: authorizer.DecisionNoOpinion,
		},
		{
			name: "Universal works too when it exists",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#universal",
					},
				},
			},
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Universal",
				},
			}),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "validates initializers on phase transition",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#foo",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
							{Name: "a", Path: "root:org:ws"},
							{Name: "b", Path: "root:org:ws"},
						},
					},
				},
			},
			attr: updateAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Foo",
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{{Name: "a", Path: "root:org:ws"}}, // b missing
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
			wantErr: true,
		},
		{
			name: "passes with all initializers or more on phase transition",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "root:org#$#foo",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
							{Name: "a", Path: "root:org:ws"},
							{Name: "b", Path: "root:org:ws"},
						},
					},
				},
			},
			attr: updateAttr(
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: "Foo",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase: tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
							{Name: "a", Path: "root:org:ws"},
							{Name: "b", Path: "root:org:ws"},
							{Name: "c", Path: "root:org:ws"},
						},
						Location: tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
						BaseURL:  "https://kcp.bigcorp.com/clusters/org:test",
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
		},
		{
			name:  "ignores different resources",
			types: nil,
			attr: admission.NewAttributesRecord(
				&tenancyv1alpha1.ClusterWorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &clusterWorkspaceTypeExists{
				Handler:    admission.NewHandler(admission.Create, admission.Update),
				typeLister: fakeClusterWorkspaceTypeLister(tt.types),
				createAuthorizer: func(clusterName logicalcluster.Name, client kubernetes.ClusterInterface) (authorizer.Authorizer, error) {
					return &fakeAuthorizer{
						tt.authzDecision,
						tt.authzError,
					}, nil
				},
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root:org")})
			if err := o.Validate(ctx, tt.attr, nil); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
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
	return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("clusterworkspacetype"), name)
}

type fakeAuthorizer struct {
	authorized authorizer.Decision
	err        error
}

func (a *fakeAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	return a.authorized, "reason", a.err
}
