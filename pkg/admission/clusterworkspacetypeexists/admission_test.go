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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clusters"

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
		workspaces  []*tenancyv1alpha1.ClusterWorkspace
		clusterName logicalcluster.Name
		a           admission.Attributes
		expectedObj runtime.Object
		wantErr     bool
	}{
		{
			name: "adds initializers during transition to initializing",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:other").withInitializer().ClusterWorkspaceType,
				newType("root:org:foo").withInitializer().extending("root:org:other").ClusterWorkspaceType,
			},
			clusterName: logicalcluster.New("root:org:ws"),
			a: updateAttr(
				newWorkspace("root:org:ws:test").withType("root:org:foo").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:    tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Location: tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:  "https://kcp.bigcorp.com/clusters/org:test",
				}).ClusterWorkspace,
				newWorkspace("root:org:ws:test").withType("root:org:foo").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
				}).ClusterWorkspace,
			),
			expectedObj: newWorkspace("root:org:ws:test").withType("root:org:foo").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
				Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
				Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"root:org:other", "root:org:foo"},
				Location:     tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
				BaseURL:      "https://kcp.bigcorp.com/clusters/org:test",
			}).ClusterWorkspace,
		},
		{
			name: "does not add initializer during transition to initializing when type has none",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:foo").ClusterWorkspaceType,
			},
			clusterName: logicalcluster.New("root:org:ws"),
			a: updateAttr(
				newWorkspace("root:org:ws:test").withType("root:org:foo").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:    tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Location: tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:  "https://kcp.bigcorp.com/clusters/org:test",
				}).ClusterWorkspace,
				newWorkspace("root:org:ws:test").withType("root:org:foo").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
				}).ClusterWorkspace,
			),
			expectedObj: newWorkspace("root:org:ws:test").withType("root:org:foo").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
				Phase:    tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
				Location: tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
				BaseURL:  "https://kcp.bigcorp.com/clusters/org:test",
			}).ClusterWorkspace,
		},
		{
			name: "does not add initializers during transition not to initializing",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:foo").withInitializer().ClusterWorkspaceType,
			},
			clusterName: logicalcluster.New("root:org:ws"),
			a: updateAttr(
				newWorkspace("root:org:ws:test").withType("root:org:foo").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
					Location:     tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:      "https://kcp.bigcorp.com/clusters/org:test",
				}).ClusterWorkspace,
				newWorkspace("root:org:ws:test").withType("root:org:foo").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{},
				}).ClusterWorkspace,
			),
			expectedObj: newWorkspace("root:org:ws:test").withType("root:org:foo").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
				Phase:    tenancyv1alpha1.ClusterWorkspacePhaseReady,
				Location: tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
				BaseURL:  "https://kcp.bigcorp.com/clusters/org:test",
			}).ClusterWorkspace,
		},
		{
			name:        "ignores different resources",
			clusterName: logicalcluster.New("root:org:ws"),
			a: admission.NewAttributesRecord(
				&unstructured.Unstructured{Object: map[string]interface{}{
					"apiVersion": tenancyv1alpha1.SchemeGroupVersion.String(),
					"kind":       "ClusterWorkspaceShard",
					"metadata": map[string]interface{}{
						"name":              "test",
						"creationTimestamp": nil,
					},
					"spec": map[string]interface{}{
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
				newType("root:org:foo").withAdditionalLabel(map[string]string{
					"new-label":      "default",
					"existing-label": "default",
				}).ClusterWorkspaceType,
			},
			clusterName: logicalcluster.New("root:org:ws"),
			a: createAttr(
				newWorkspace("root:org:ws:test").withType("root:org:foo").withLabels(map[string]string{
					"existing-label": "non-default",
				}).ClusterWorkspace,
			),
			expectedObj: newWorkspace("root:org:ws:test").withType("root:org:foo").withLabels(map[string]string{
				"new-label":      "default",
				"existing-label": "non-default",
			}).ClusterWorkspace,
		},
		{
			name: "adds default workspace type if missing",
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:parent").withDefault("root:org:foo").ClusterWorkspaceType,
				newType("root:org:foo").ClusterWorkspaceType,
			},
			clusterName: logicalcluster.New("root:org:ws"),
			a:           createAttr(newWorkspace("root:org:ws:test").ClusterWorkspace),
			expectedObj: newWorkspace("root:org:ws:test").withType("root:org:foo").ClusterWorkspace,
		},
		{
			name: "adds default workspace type if missing in root",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:root").withDefault("root:organization").ClusterWorkspaceType,
				newType("root:organization").ClusterWorkspaceType,
			},
			clusterName: logicalcluster.New("root"),
			a:           createAttr(newWorkspace("root:test").ClusterWorkspace),
			expectedObj: newWorkspace("root:test").withType("root:organization").ClusterWorkspace,
		},
		{
			name: "resolves path of incomplete type reference in local workspace",
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:parent").withDefault("root:org:foo").ClusterWorkspaceType,
				newType("root:org:foo").ClusterWorkspaceType,
			},
			clusterName: logicalcluster.New("root:org:ws"),
			a:           createAttr(newWorkspace("root:org:ws:test").withType("foo").ClusterWorkspace),
			expectedObj: newWorkspace("root:org:ws:test").withType("root:org:foo").ClusterWorkspace,
		},
		{
			name: "resolves path of incomplete type reference in the hierarchy",
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:parent").withDefault("root:org:foo").ClusterWorkspaceType,
				newType("root:foo").ClusterWorkspaceType,
			},
			clusterName: logicalcluster.New("root:org:ws"),
			a:           createAttr(newWorkspace("root:org:ws:test").withType("foo").ClusterWorkspace),
			expectedObj: newWorkspace("root:org:ws:test").withType("root:foo").ClusterWorkspace,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			typeLister := fakeClusterWorkspaceTypeLister(tt.types)
			o := &clusterWorkspaceTypeExists{
				Handler:         admission.NewHandler(admission.Create, admission.Update),
				typeLister:      typeLister,
				workspaceLister: fakeClusterWorkspaceLister(tt.workspaces),
				transitiveTypeResolver: transitiveTypeResolver{
					getter: func(cluster logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
						return typeLister.Get(clusters.ToClusterAwareKey(cluster, name))
					},
				},
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
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:universal").ClusterWorkspaceType,
				newType("root:org:parent").allowingChild("root:org:foo").ClusterWorkspaceType,
				newType("root:org:foo").allowingParent("root:org:parent").ClusterWorkspaceType,
			},
			attr:          createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").ClusterWorkspace),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "passes create if unqualified type can be resolve locally",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:universal").ClusterWorkspaceType,
				newType("root:org:foo").ClusterWorkspaceType,
				newType("root:foo").ClusterWorkspaceType,
				newType("root").ClusterWorkspaceType,
			},
			attr:    createAttr(newWorkspace("root:org:ws:test").withType("foo").ClusterWorkspace),
			wantErr: true,
		},
		{
			name: "passes create if parent type allows all children",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:parent").ClusterWorkspaceType,
				newType("root:org:foo").allowingParent("root:org:parent").ClusterWorkspaceType,
			},
			attr:          createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").ClusterWorkspace),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "passes create if child type allows all parents",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:parent").allowingChild("root:org:foo").ClusterWorkspaceType,
				newType("root:org:foo").ClusterWorkspaceType,
			},
			attr:          createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").ClusterWorkspace),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "passes create if parent type allows an alias of the child type",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:fooalias").ClusterWorkspaceType,
				newType("root:org:parent").allowingChild("root:org:fooalias").ClusterWorkspaceType,
				newType("root:org:foo").extending("root:org:fooalias").ClusterWorkspaceType,
			},
			attr:          createAttr(newWorkspace("root:org:ws:Test").withType("root:org:foo").ClusterWorkspace),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "passes create if child type allows an alias of the parent type",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:parentalias").ClusterWorkspaceType,
				newType("root:org:parent").extending("root:org:parentalias").ClusterWorkspaceType,
				newType("root:org:foo").allowingParent("root:org:parentalias").ClusterWorkspaceType,
			},
			attr:          createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").ClusterWorkspace),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name:       "passes create if parent type missing but parent workspace is root",
			path:       logicalcluster.New("root"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:universal").ClusterWorkspaceType,
				newType("root:foo").ClusterWorkspaceType,
			},
			attr:          createAttr(newWorkspace("root:test").withType("root:foo").ClusterWorkspace),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name:    "fails if type does not exist",
			path:    logicalcluster.New("root:org:ws"),
			attr:    createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").ClusterWorkspace),
			wantErr: true,
		},
		{
			name: "fails if type is root:root",
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				tenancyv1alpha1.RootWorkspaceType,
			},
			path:    logicalcluster.New("root:org:ws"),
			attr:    createAttr(newWorkspace("root:test").withType("root:root").ClusterWorkspace),
			wantErr: true,
		},
		{
			name: "fails if type only exists in unrelated workspace",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:parent").allowingChild("root:org:foo").ClusterWorkspaceType,
				newType("root:bigcorp:foo").ClusterWorkspaceType,
			},
			attr:    createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").ClusterWorkspace),
			wantErr: true,
		},
		{
			name: "fails if parent type doesn't allow child workspaces",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:parent").ClusterWorkspaceType,
				newType("root:org:foo").ClusterWorkspaceType,
			},
			attr:    createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").ClusterWorkspace),
			wantErr: true,
		},
		{
			name: "fails if child type doesn't allow parent workspaces",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:parent").ClusterWorkspaceType,
				newType("root:org:foo").ClusterWorkspaceType,
			},
			attr:    createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").ClusterWorkspace),
			wantErr: true,
		},
		{
			name:          "fails if not allowed",
			path:          logicalcluster.New("root:org:ws"),
			attr:          createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").ClusterWorkspace),
			authzDecision: authorizer.DecisionNoOpinion,
			wantErr:       true,
		},
		{
			name: "fails if denied",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:parent").allowingChild("root:org:foo").ClusterWorkspaceType,
				newType("root:org:foo").ClusterWorkspaceType,
			},
			attr:          createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").ClusterWorkspace),
			authzDecision: authorizer.DecisionDeny,
			wantErr:       true,
		},
		{
			name: "fails if authz error",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:parent").allowingChild("root:org:foo").ClusterWorkspaceType,
				newType("root:org:foo").ClusterWorkspaceType,
			},
			attr:       createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").ClusterWorkspace),
			authzError: errors.New("authorizer error"),
			wantErr:    true,
		},
		{
			name: "validates initializers on phase transition",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:parent").allowingChild("root:org:foo").ClusterWorkspaceType,
				newType("root:org:foo").withInitializer().ClusterWorkspaceType,
			},
			attr: updateAttr(
				newWorkspace("root:org:ws:test").withType("root:org:foo").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{}, // root:org:foo missing
				}).ClusterWorkspace,
				newWorkspace("root:org:ws:test").withType("root:org:foo").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase: tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
				}).ClusterWorkspace,
			),
			wantErr: true,
		},
		{
			name: "passes with all initializers or more on phase transition",
			path: logicalcluster.New("root:org:ws"),
			workspaces: []*tenancyv1alpha1.ClusterWorkspace{
				newWorkspace("root:org:ws").withType("root:org:parent").ClusterWorkspace,
			},
			types: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:org:parent").allowingChild("root:org:foo").ClusterWorkspaceType,
				newType("root:org:foo").withInitializer().ClusterWorkspaceType,
			},
			attr: updateAttr(
				newWorkspace("root:org:ws:test").withType("root:org:foo").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:        tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.ClusterWorkspaceInitializer{"root:org:foo", "unrelated"},
					Location:     tenancyv1alpha1.ClusterWorkspaceLocation{Current: "somewhere"},
					BaseURL:      "https://kcp.bigcorp.com/clusters/org:test",
				}).ClusterWorkspace,
				newWorkspace("root:org:ws:test").withType("root:org:foo").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase: tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
				}).ClusterWorkspace,
			),
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
			typeLister := fakeClusterWorkspaceTypeLister(tt.types)
			o := &clusterWorkspaceTypeExists{
				Handler:         admission.NewHandler(admission.Create, admission.Update),
				typeLister:      typeLister,
				workspaceLister: fakeClusterWorkspaceLister(tt.workspaces),
				createAuthorizer: func(clusterName logicalcluster.Name, client kubernetes.Interface) (authorizer.Authorizer, error) {
					return &fakeAuthorizer{
						tt.authzDecision,
						tt.authzError,
					}, nil
				},
				transitiveTypeResolver: transitiveTypeResolver{
					getter: func(cluster logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
						return typeLister.Get(clusters.ToClusterAwareKey(cluster, name))
					},
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

func TestTransitiveTypeResolverResolve(t *testing.T) {
	tests := []struct {
		name    string
		types   map[string]*tenancyv1alpha1.ClusterWorkspaceType
		input   *tenancyv1alpha1.ClusterWorkspaceType
		want    sets.String
		wantErr bool
	}{
		{
			name:  "no other types",
			input: newType("root:org:ws").ClusterWorkspaceType,
			want:  sets.NewString("root:org:ws"),
		},
		{
			name:    "error on self-reference",
			input:   newType("root:org:ws").extending("root:org:ws").ClusterWorkspaceType,
			wantErr: true,
		},
		{
			name: "extending types",
			types: map[string]*tenancyv1alpha1.ClusterWorkspaceType{
				"root:universal":    newType("root:universal").ClusterWorkspaceType,
				"root:organization": newType("root:organization").ClusterWorkspaceType,
			},
			input: newType("root:org:type").extending("root:universal").extending("root:organization").ClusterWorkspaceType,
			want:  sets.NewString("root:universal", "root:organization", "root:org:type"),
		},
		{
			name:    "missing types",
			input:   newType("root:org:type").extending("root:universal").extending("root:organization").ClusterWorkspaceType,
			want:    sets.NewString("root:universal", "root:organization", "root:org:type"),
			wantErr: true,
		},
		{
			name: "extending types transitively",
			types: map[string]*tenancyv1alpha1.ClusterWorkspaceType{
				"root:universal":    newType("root:universal").ClusterWorkspaceType,
				"root:organization": newType("root:organization").extending("root:universal").ClusterWorkspaceType,
			},
			input: newType("root:org:type").extending("root:organization").ClusterWorkspaceType,
			want:  sets.NewString("root:universal", "root:organization", "root:org:type"),
		},
		{
			name: "extending types transitively, one missing",
			types: map[string]*tenancyv1alpha1.ClusterWorkspaceType{
				"root:organization": newType("root:organization").extending("root:universal").ClusterWorkspaceType,
			},
			input:   newType("root:org:type").extending("root:organization").ClusterWorkspaceType,
			want:    sets.NewString("root:universal", "root:organization", "root:org:type"),
			wantErr: true,
		},
		{
			name: "cycle",
			types: map[string]*tenancyv1alpha1.ClusterWorkspaceType{
				"root:a": newType("root:a").extending("root:b").ClusterWorkspaceType,
				"root:b": newType("root:b").extending("root:c").ClusterWorkspaceType,
				"root:c": newType("root:c").extending("root:a").ClusterWorkspaceType,
			},
			input:   newType("root:org:type").extending("root:a").ClusterWorkspaceType,
			want:    sets.NewString("root:universal", "root:organization", "root:org:type"),
			wantErr: true,
		},
		{
			name: "cycle starting at input",
			types: map[string]*tenancyv1alpha1.ClusterWorkspaceType{
				"root:b": newType("root:b").extending("root:c").ClusterWorkspaceType,
				"root:c": newType("root:c").extending("root:a").ClusterWorkspaceType,
			},
			input:   newType("root:org:a").extending("root:b").ClusterWorkspaceType,
			want:    sets.NewString("root:universal", "root:organization", "root:org:type"),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &transitiveTypeResolver{
				getter: func(cluster logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspaceType, error) {
					if t, found := tt.types[cluster.Join(name).String()]; found {
						return t, nil
					}
					return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("clusterworkspacetypes"), name)
				},
			}
			gotTypes, err := r.Resolve(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Resolve() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil {
				got := sets.NewString()
				for _, t := range gotTypes {
					got.Insert(logicalcluster.From(t).Join(t.Name).String())
				}
				if !got.Equal(tt.want) {
					t.Errorf("Missing: %s\nUnexpected: %s", tt.want.Difference(got).List(), got.Difference(tt.want).List())
				}
			}
		})
	}
}

func TestValidateAllowedParents(t *testing.T) {
	tests := []struct {
		name          string
		parentAliases []*tenancyv1alpha1.ClusterWorkspaceType
		childAliases  []*tenancyv1alpha1.ClusterWorkspaceType
		parentType    string
		childType     string
		wantErr       string
	}{
		{
			name:       "no child",
			childType:  "",
			parentType: "",
			wantErr:    "",
		},
		{
			name:       "no parents",
			childType:  "root:a",
			parentType: "root:c",
			childAliases: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:a").allowingParent("root:b").ClusterWorkspaceType,
			},
			wantErr: "workspace type root:a only allows [root:b] parent workspaces, but parent type root:c only implements []",
		},
		{
			name:       "no parents, any allowed parent",
			childType:  "root:a",
			parentType: "root:b",
			childAliases: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:a").ClusterWorkspaceType,
			},
		},
		{
			name:       "all parents allowed",
			childType:  "root:a",
			parentType: "root:a",
			parentAliases: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:a").ClusterWorkspaceType,
				newType("root:b").ClusterWorkspaceType,
			},
			childAliases: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:a").allowingParent("root:a").allowingParent("root:d").ClusterWorkspaceType,
				newType("root:b").allowingParent("root:b").allowingParent("root:d").ClusterWorkspaceType,
				newType("root:c").allowingParent("root:a").allowingParent("root:b").allowingParent("root:d").ClusterWorkspaceType,
			},
			wantErr: "",
		},
		{
			name:       "missing parent alias",
			childType:  "root:a",
			parentType: "root:a",
			parentAliases: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:a").ClusterWorkspaceType,
			},
			childAliases: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:a").allowingParent("root:a").allowingParent("root:d").ClusterWorkspaceType,
				newType("root:b").allowingParent("root:b").allowingParent("root:d").ClusterWorkspaceType,
			},
			wantErr: "workspace type root:a extends root:b, which only allows [root:b root:d] parent workspaces, but parent type root:a only implements [root:a]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateAllowedParents(tt.parentAliases, tt.childAliases, tt.parentType, tt.childType); (err != nil) != (tt.wantErr != "") {
				t.Errorf("validateAllowedParents() error = %v, wantErr %q", err, tt.wantErr)
			} else if tt.wantErr != "" {
				require.Containsf(t, err.Error(), tt.wantErr, "expected error to contain %q, got %q", tt.wantErr, err)
			}

			// inverse child/parent inputs for validateAllowedChildren, fully symmetric
			wantErr := strings.Replace(tt.wantErr, "parent", "child", -1)
			childAliases := make([]*tenancyv1alpha1.ClusterWorkspaceType, len(tt.parentAliases))
			for i, parent := range tt.parentAliases {
				parent = parent.DeepCopy()
				parent.Spec.LimitAllowedChildren, parent.Spec.LimitAllowedParents = parent.Spec.LimitAllowedParents, parent.Spec.LimitAllowedChildren
				childAliases[i] = parent
			}
			parentAliases := make([]*tenancyv1alpha1.ClusterWorkspaceType, len(tt.childAliases))
			for i, child := range tt.childAliases {
				child := child.DeepCopy()
				child.Spec.LimitAllowedChildren, child.Spec.LimitAllowedParents = child.Spec.LimitAllowedParents, child.Spec.LimitAllowedChildren
				parentAliases[i] = child
			}
			parentType, childType := tt.childType, tt.parentType
			if err := validateAllowedChildren(parentAliases, childAliases, parentType, childType); (err != nil) != (wantErr != "") {
				t.Errorf("validateAllowedChildren() error = %v, wantErr %q", err, wantErr)
			} else if tt.wantErr != "" {
				require.Containsf(t, err.Error(), wantErr, "expected error to contain %q, got %q", wantErr, err)
			}
		})
	}
}

func TestValidateAllowedChildren(t *testing.T) {
	tests := []struct {
		name          string
		parentAliases []*tenancyv1alpha1.ClusterWorkspaceType
		childAliases  []*tenancyv1alpha1.ClusterWorkspaceType
		parentType    string
		childType     string
		wantErr       string
	}{
		{
			name:       "some type disallows children",
			childType:  "root:a",
			parentType: "root:a",
			parentAliases: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:a").ClusterWorkspaceType,
				newType("root:b").disallowingChildren().ClusterWorkspaceType,
			},
			childAliases: []*tenancyv1alpha1.ClusterWorkspaceType{
				newType("root:a").allowingParent("root:a").allowingParent("root:d").ClusterWorkspaceType,
				newType("root:b").allowingParent("root:b").allowingParent("root:d").ClusterWorkspaceType,
				newType("root:c").allowingParent("root:a").allowingParent("root:b").allowingParent("root:d").ClusterWorkspaceType,
			},
			wantErr: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateAllowedParents(tt.parentAliases, tt.childAliases, tt.parentType, tt.childType); (err != nil) != (tt.wantErr != "") {
				t.Errorf("validateAllowedParents() error = %v, wantErr %q", err, tt.wantErr)
			} else if tt.wantErr != "" {
				require.Containsf(t, err.Error(), tt.wantErr, "expected error to contain %q, got %q", tt.wantErr, err)
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
			Name:                      name,
			ZZZ_DeprecatedClusterName: path.String(),
		},
	}}
}

func (b builder) extending(qualifiedName string) builder {
	path, name := logicalcluster.New(qualifiedName).Split()
	b.Spec.Extend.With = append(b.Spec.Extend.With, tenancyv1alpha1.ClusterWorkspaceTypeReference{Path: path.String(), Name: tenancyv1alpha1.ClusterWorkspaceTypeName(name)})
	return b
}

func (b builder) allowingParent(qualifiedName string) builder {
	path, name := logicalcluster.New(qualifiedName).Split()
	if b.Spec.LimitAllowedParents == nil {
		b.Spec.LimitAllowedParents = &tenancyv1alpha1.ClusterWorkspaceTypeSelector{}
	}
	b.Spec.LimitAllowedParents.Types = append(b.Spec.LimitAllowedParents.Types, tenancyv1alpha1.ClusterWorkspaceTypeReference{Path: path.String(), Name: tenancyv1alpha1.ClusterWorkspaceTypeName(name)})
	return b
}

func (b builder) allowingChild(qualifiedName string) builder {
	path, name := logicalcluster.New(qualifiedName).Split()
	if b.Spec.LimitAllowedChildren == nil {
		b.Spec.LimitAllowedChildren = &tenancyv1alpha1.ClusterWorkspaceTypeSelector{}
	}
	b.Spec.LimitAllowedChildren.Types = append(b.Spec.LimitAllowedChildren.Types, tenancyv1alpha1.ClusterWorkspaceTypeReference{Path: path.String(), Name: tenancyv1alpha1.ClusterWorkspaceTypeName(name)})
	return b
}

func (b builder) withDefault(qualifiedName string) builder {
	path, name := logicalcluster.New(qualifiedName).Split()
	b.Spec.DefaultChildWorkspaceType = &tenancyv1alpha1.ClusterWorkspaceTypeReference{
		Path: path.String(),
		Name: tenancyv1alpha1.ClusterWorkspaceTypeName(name),
	}
	return b
}

func (b builder) disallowingChildren() builder {
	b.ClusterWorkspaceType.Spec.LimitAllowedChildren = &tenancyv1alpha1.ClusterWorkspaceTypeSelector{
		None: true,
	}
	return b
}

func (b builder) withInitializer() builder {
	b.ClusterWorkspaceType.Spec.Initializer = true
	return b
}

func (b builder) withAdditionalLabel(labels map[string]string) builder {
	b.ClusterWorkspaceType.Spec.AdditionalWorkspaceLabels = labels
	return b
}

type wsBuilder struct {
	*tenancyv1alpha1.ClusterWorkspace
}

func newWorkspace(qualifiedName string) wsBuilder {
	path, name := logicalcluster.New(qualifiedName).Split()
	return wsBuilder{ClusterWorkspace: &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:                      name,
			ZZZ_DeprecatedClusterName: path.String(),
		},
	}}
}

func (b wsBuilder) withType(qualifiedName string) wsBuilder {
	path, name := logicalcluster.New(qualifiedName).Split()
	b.Spec.Type = tenancyv1alpha1.ClusterWorkspaceTypeReference{
		Path: path.String(),
		Name: tenancyv1alpha1.ClusterWorkspaceTypeName(name),
	}
	return b
}

func (b wsBuilder) withStatus(status tenancyv1alpha1.ClusterWorkspaceStatus) wsBuilder {
	b.Status = status
	return b
}

func (b wsBuilder) withLabels(labels map[string]string) wsBuilder {
	b.Labels = labels
	return b
}
