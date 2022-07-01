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

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
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
	"k8s.io/client-go/tools/clusters"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

func createAttrWithUser(obj *tenancyv1alpha1.ClusterWorkspace, info user.Info) admission.Attributes {
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
		info,
	)
}

func createAttr(obj *tenancyv1alpha1.ClusterWorkspace) admission.Attributes {
	return createAttrWithUser(obj, &user.DefaultInfo{})
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
		workspaces  []*tenancyv1alpha1.ClusterWorkspace
		a           admission.Attributes
		expectedObj runtime.Object
		wantErr     bool
	}{
		{
			name: "adds initializers during transition to initializing",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{
							"root:org:Foo",
							"root:org:Other",
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:    tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Location: tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:  "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Foo",
							Path: "root:org",
						},
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
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"root:org:Foo", "root:org:Other"},
					Location:     tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:      "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
		},
		{
			name: "forbids when type does not have extensions resolved",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   tenancyv1alpha1.ClusterWorkspaceTypeExtensionsResolved,
								Status: corev1.ConditionFalse,
							},
						},
					},
				},
			},
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:    tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Location: tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:  "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Foo",
							Path: "root:org",
						},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase:        tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					},
				}),
			wantErr: true,
		},
		{
			name: "does not add initializer during transition to initializing when type has none",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:    tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Location: tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:  "https://kcp.bigcorp.com/clusters/org:test",
				},
			},
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Foo",
							Path: "root:org",
						},
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
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:    tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
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
						Name:        "foo",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializer: true,
					},
				},
			},
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
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
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Foo",
							Path: "root:org",
						},
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
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:    tenancyv1alpha1.ClusterWorkspacePhaseReady,
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
						Name:        "foo",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AdditionalWorkspaceLabels: map[string]string{
							"new-label":      "default",
							"existing-label": "default",
						},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
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
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}),
			expectedObj: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"tenancy.kcp.dev/owner": `{}`,
					},
					Labels: map[string]string{
						"new-label":      "default",
						"existing-label": "non-default",
					},
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			},
		},
		{
			name: "adds default workspace type if missing",
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "ws",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Parent",
							Path: "root:org",
						},
					},
				},
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "parent",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						DefaultChildWorkspaceType: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Foo",
							Path: "root:org",
						},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			a: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{},
			}),
			expectedObj: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"tenancy.kcp.dev/owner": `{}`,
					},
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			},
		},
		{
			name: "adds user information on create",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			a: createAttrWithUser(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
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
			expectedObj: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"tenancy.kcp.dev/owner": `{"username":"someone","uid":"id","groups":["a","b"],"extra":{"one":["1","01"]}}`,
					},
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
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
			o := &clusterWorkspaceTypeExists{
				Handler:         admission.NewHandler(admission.Create, admission.Update),
				typeLister:      fakeClusterWorkspaceTypeLister(tt.types),
				workspaceLister: fakeClusterWorkspaceLister(tt.workspaces),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root:org:ws")})
			if err := o.Admit(ctx, tt.a, nil); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
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
		name       string
		types      []*tenancyv1alpha1.ClusterWorkspaceType
		workspaces []*tenancyv1alpha1.ClusterWorkspace
		attr       admission.Attributes
		path       logicalcluster.Name

		authzDecision authorizer.Decision
		authzError    error

		wantErr bool
	}{
		{
			name: "passes create if type exists",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "ws",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Parent",
							Path: "root:org",
						},
					},
				},
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "parent",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Foo", Path: "root:org"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Parent"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Parent", Path: "root:org"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Foo"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "passes create if parent type allows all children",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "ws",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Parent",
							Path: "root:org",
						},
					},
				},
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "parent",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Any"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Parent"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Parent", Path: "root:org"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Foo"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "passes create if child type allows all parents",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "ws",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Parent",
							Path: "root:org",
						},
					},
				},
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "parent",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Foo", Path: "root:org"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Parent"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Any"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Foo"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "passes create if parent type allows an alias of the child type",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "ws",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Parent",
							Path: "root:org",
						},
					},
				},
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "parent",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "FooAlias", Path: "root:org"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Parent"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Any"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Foo"},
							{Path: "root:org", Name: "FooAlias"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "passes create if child type allows an alias of the parent type",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "ws",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Parent",
							Path: "root:org",
						},
					},
				},
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "parent",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Any"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Parent"},
							{Path: "root:org", Name: "ParentAlias"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "ParentAlias", Path: "root:org"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Foo"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name:       "passes create if parent type missing but parent workspace is root",
			path:       logicalcluster.New("root"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Any"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root", Name: "Foo"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root",
					},
				},
			}),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "fails if type does not exist",
			path: logicalcluster.New("root:org:ws"),
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}),
			wantErr: true,
		},
		{
			name: "fails if type only exists in unrelated workspace",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "ws",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Parent",
							Path: "root:org",
						},
					},
				},
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "parent",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Foo", Path: "root:org"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Parent"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:bigcorp",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:bigcorp", Name: "Foo"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}),
			wantErr: true,
		},
		{
			name: "fails if parent type doesn't allow child workspaces",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "ws",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Parent",
							Path: "root:org",
						},
					},
				},
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "parent",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Parent"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Any"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Foo"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}),
			wantErr: true,
		},
		{
			name: "fails if child type doesn't allow parent workspaces",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "ws",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Parent",
							Path: "root:org",
						},
					},
				},
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "parent",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Any"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Parent"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Foo"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}),
			wantErr: true,
		},
		{
			name: "fails if not allowed",
			path: logicalcluster.New("root:org:ws"),
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}),
			authzDecision: authorizer.DecisionNoOpinion,
			wantErr:       true,
		},
		{
			name: "fails if denied",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "ws",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Parent",
							Path: "root:org",
						},
					},
				},
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "parent",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Foo", Path: "root:org"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Parent"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Foo"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}),
			authzDecision: authorizer.DecisionDeny,
			wantErr:       true,
		},
		{
			name: "fails if authz error",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "ws",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Parent",
							Path: "root:org",
						},
					},
				},
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "parent",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Foo", Path: "root:org"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Parent"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
				},
			},
			attr: createAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}),
			authzError: errors.New("authorizer error"),
			wantErr:    true,
		},
		{
			name: "validates initializers on phase transition",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "ws",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Parent",
							Path: "root:org",
						},
					},
				},
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "parent",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Foo", Path: "root:org"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Parent"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializer:                 true,
						AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Any"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Foo"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
			},
			attr: updateAttr(&tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{}, // root:org:Foo missing
				},
			},
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Foo",
							Path: "root:org",
						},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase: tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
					},
				}),
			wantErr: true,
		},
		{
			name: "passes with all initializers or more on phase transition",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "ws",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Parent",
							Path: "root:org",
						},
					},
				},
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "parent",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Foo", Path: "root:org"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Parent"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "foo",
						ClusterName: "root:org",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
						Initializer:                 true,
						AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{{Name: "Any"}},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceTypeStatus{
						TypeAliases: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:org", Name: "Foo"},
						},
						Conditions: []conditionsv1alpha1.Condition{
							{
								Type:   conditionsv1alpha1.ReadyCondition,
								Status: corev1.ConditionTrue,
							},
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
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Foo",
							Path: "root:org",
						},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
						Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"root:org:Foo", "unrelated"},
						Location:     tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
						BaseURL:      "https://kcp.bigcorp.com/clusters/org:test",
					},
				},
				&tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
						Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: "Foo",
							Path: "root:org",
						},
					},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase: tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
					},
				}),
		},
		{
			name:  "ignores different resources",
			path:  logicalcluster.New("root:org:ws"),
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
				Handler:         admission.NewHandler(admission.Create, admission.Update),
				typeLister:      fakeClusterWorkspaceTypeLister(tt.types),
				workspaceLister: fakeClusterWorkspaceLister(tt.workspaces),
				createAuthorizer: func(clusterName logicalcluster.Name, client kubernetes.ClusterInterface) (authorizer.Authorizer, error) {
					return &fakeAuthorizer{
						tt.authzDecision,
						tt.authzError,
					}, nil
				},
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: tt.path})
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
		if clusters.ToClusterAwareKey(logicalcluster.From(t), t.Name) == name {
			return t, nil
		}
	}
	return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("clusterworkspacetype"), name)
}

type fakeClusterWorkspaceLister []*tenancyv1alpha1.ClusterWorkspace

func (l fakeClusterWorkspaceLister) List(selector labels.Selector) (ret []*tenancyv1alpha1.ClusterWorkspace, err error) {
	return l.ListWithContext(context.Background(), selector)
}

func (l fakeClusterWorkspaceLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*tenancyv1alpha1.ClusterWorkspace, err error) {
	return l, nil
}

func (l fakeClusterWorkspaceLister) Get(name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
	return l.GetWithContext(context.Background(), name)
}

func (l fakeClusterWorkspaceLister) GetWithContext(ctx context.Context, name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
	for _, t := range l {
		if clusters.ToClusterAwareKey(logicalcluster.From(t), t.Name) == name {
			return t, nil
		}
	}
	return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("clusterworkspace"), name)
}

type fakeAuthorizer struct {
	authorized authorizer.Decision
	err        error
}

func (a *fakeAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	return a.authorized, "reason", a.err
}
