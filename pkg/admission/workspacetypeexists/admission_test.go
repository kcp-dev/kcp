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

package workspacetypeexists

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
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

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	"github.com/kcp-dev/kcp/pkg/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	corev1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/core/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

func createAttr(obj *tenancyv1beta1.Workspace) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(obj),
		nil,
		tenancyv1alpha1.Kind("Workspace").WithVersion("v1beta1"),
		"",
		obj.Name,
		tenancyv1alpha1.Resource("workspaces").WithVersion("v1beta1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func TestAdmit(t *testing.T) {
	tests := []struct {
		name            string
		types           []*tenancyv1alpha1.WorkspaceType
		logicalClusters []*corev1alpha1.LogicalCluster
		clusterName     logicalcluster.Name
		a               admission.Attributes
		expectedObj     runtime.Object
		wantErr         bool
	}{
		{
			name:        "ignores different resources",
			clusterName: logicalcluster.Name("root:org:ws"),
			a: admission.NewAttributesRecord(
				&unstructured.Unstructured{Object: map[string]interface{}{
					"apiVersion": corev1alpha1.SchemeGroupVersion.String(),
					"kind":       "Shard",
					"metadata": map[string]interface{}{
						"name":              "test",
						"creationTimestamp": nil,
					},
					"spec": map[string]interface{}{
						"baseURL": "",
					},
					"status": map[string]interface{}{},
				}},
				nil,
				corev1alpha1.Kind("Shard").WithVersion("v1alpha1"),
				"",
				"test",
				corev1alpha1.Resource("shards").WithVersion("v1alpha1"),
				"",
				admission.Create,
				&metav1.CreateOptions{},
				false,
				&user.DefaultInfo{},
			),
			expectedObj: &corev1alpha1.Shard{
				TypeMeta: metav1.TypeMeta{
					APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(),
					Kind:       "Shard",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			},
		},
		{
			name: "adds additional workspace labels if missing",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").withType("root:org", "parent").LogicalCluster,
			},
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:org:foo").withAdditionalLabel(map[string]string{
					"new-label":      "default",
					"existing-label": "default",
				}).WorkspaceType,
			},
			clusterName: logicalcluster.Name("root:org:ws"),
			a: createAttr(
				newWorkspace("root:org:ws:test").withType("root:org:foo").withLabels(map[string]string{
					"existing-label": "non-default",
				}).Workspace,
			),
			expectedObj: newWorkspace("root:org:ws:test").withType("root:org:foo").withLabels(map[string]string{
				"new-label":      "default",
				"existing-label": "non-default",
			}).Workspace,
		},
		{
			name: "adds default workspace type if missing",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").withType("root:org", "parent").LogicalCluster,
			},
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:org:parent").withDefault("root:org:foo").WorkspaceType,
				newType("root:org:foo").WorkspaceType,
			},
			clusterName: logicalcluster.Name("root:org:ws"),
			a:           createAttr(newWorkspace("root:org:ws:test").Workspace),
			expectedObj: newWorkspace("root:org:ws:test").withType("root:org:foo").Workspace,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			typeLister := fakeWorkspaceTypeClusterLister(tt.types)

			o := &workspacetypeExists{
				Handler:                admission.NewHandler(admission.Create, admission.Update),
				getType:                getType(tt.types),
				typeLister:             typeLister,
				logicalClusterLister:   fakeLogicalClusterClusterLister(tt.logicalClusters),
				transitiveTypeResolver: NewTransitiveTypeResolver(typeLister.GetByPath),
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
		name            string
		types           []*tenancyv1alpha1.WorkspaceType
		logicalClusters []*corev1alpha1.LogicalCluster
		attr            admission.Attributes
		clusterName     logicalcluster.Name

		authzDecision authorizer.Decision
		authzError    error

		wantErr bool
	}{
		{
			name:        "passes create if type exists",
			clusterName: logicalcluster.Name("root:org:ws"),
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").withType("root:org", "parent").LogicalCluster,
			},
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:universal").WorkspaceType,
				newType("root:org:parent").allowingChild("root:org:foo").WorkspaceType,
				newType("root:org:foo").allowingParent("root:org:parent").WorkspaceType,
			},
			attr:          createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").Workspace),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name:        "fails create if type reference misses path",
			clusterName: logicalcluster.Name("root:org:ws"),
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").withType("root:org", "foo").LogicalCluster,
			},
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:org:ws:foo").WorkspaceType,
				newType("root:org:foo").WorkspaceType,
			},
			attr:    createAttr(newWorkspace("root:org:ws:test").withType("foo").Workspace),
			wantErr: true,
		},
		{
			name:        "fails create if type reference misses cluster",
			clusterName: logicalcluster.Name("root:org:ws"),
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").withType("root:org", "foo").LogicalCluster,
			},
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:org:ws:foo").WorkspaceType,
				newType("root:org:foo").WorkspaceType,
			},
			attr:    createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").Workspace),
			wantErr: true,
		},
		{
			name:        "passes create if parent type allows all children",
			clusterName: logicalcluster.Name("root:org:ws"),
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").withType("root:org", "parent").LogicalCluster,
			},
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:org:parent").WorkspaceType,
				newType("root:org:foo").allowingParent("root:org:parent").WorkspaceType,
			},
			attr:          createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").Workspace),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name:        "passes create if child type allows all parents",
			clusterName: logicalcluster.Name("root:org:ws"),
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").withType("root:org", "parent").LogicalCluster,
			},
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:org:parent").allowingChild("root:org:foo").WorkspaceType,
				newType("root:org:foo").WorkspaceType,
			},
			attr:          createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").Workspace),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name:        "passes create if parent type allows an alias of the child type",
			clusterName: logicalcluster.Name("root:org:ws"),
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").withType("root:org", "parent").LogicalCluster,
			},
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:org:fooalias").WorkspaceType,
				newType("root:org:parent").allowingChild("root:org:fooalias").WorkspaceType,
				newType("root:org:foo").extending("root:org:fooalias").WorkspaceType,
			},
			attr:          createAttr(newWorkspace("root:org:ws:Test").withType("root:org:foo").Workspace),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name:        "passes create if child type allows an alias of the parent type",
			clusterName: logicalcluster.Name("root:org:ws"),
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").withType("root:org", "parent").LogicalCluster,
			},
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:org:parentalias").WorkspaceType,
				newType("root:org:parent").extending("root:org:parentalias").WorkspaceType,
				newType("root:org:foo").allowingParent("root:org:parentalias").WorkspaceType,
			},
			attr:          createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").Workspace),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name:        "fails if type does not exist",
			clusterName: logicalcluster.Name("root:org:ws"),
			attr:        createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").Workspace),
			wantErr:     true,
		},
		{
			name: "fails if type is root:root",
			types: []*tenancyv1alpha1.WorkspaceType{
				tenancyv1alpha1.RootWorkspaceType,
			},
			clusterName: logicalcluster.Name("root:org:ws"),
			attr:        createAttr(newWorkspace("root:test").withType("root:root").Workspace),
			wantErr:     true,
		},
		{
			name:        "fails if type only exists in unrelated workspace",
			clusterName: logicalcluster.Name("root:org:ws"),
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").withType("root:org", "parent").LogicalCluster,
			},
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:org:parent").allowingChild("root:org:foo").WorkspaceType,
				newType("root:bigcorp:foo").WorkspaceType,
			},
			attr:    createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").Workspace),
			wantErr: true,
		},
		{
			name:        "fails if parent type doesn't allow child workspaces",
			clusterName: logicalcluster.Name("root:org:ws"),
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").withType("root:org", "parent").LogicalCluster,
			},
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:org:parent").WorkspaceType,
				newType("root:org:foo").WorkspaceType,
			},
			attr:    createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").Workspace),
			wantErr: true,
		},
		{
			name:        "fails if child type doesn't allow parent workspaces",
			clusterName: logicalcluster.Name("root:org:ws"),
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").withType("root:org", "parent").LogicalCluster,
			},
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:org:parent").WorkspaceType,
				newType("root:org:foo").WorkspaceType,
			},
			attr:    createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").Workspace),
			wantErr: true,
		},
		{
			name:          "fails if not allowed",
			clusterName:   logicalcluster.Name("root:org:ws"),
			attr:          createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").Workspace),
			authzDecision: authorizer.DecisionNoOpinion,
			wantErr:       true,
		},
		{
			name:        "fails if denied",
			clusterName: logicalcluster.Name("root:org:ws"),
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").withType("root:org", "parent").LogicalCluster,
			},
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:org:parent").allowingChild("root:org:foo").WorkspaceType,
				newType("root:org:foo").WorkspaceType,
			},
			attr:          createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").Workspace),
			authzDecision: authorizer.DecisionDeny,
			wantErr:       true,
		},
		{
			name:        "fails if authz error",
			clusterName: logicalcluster.Name("root:org:ws"),
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").withType("root:org", "parent").LogicalCluster,
			},
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:org:parent").allowingChild("root:org:foo").WorkspaceType,
				newType("root:org:foo").WorkspaceType,
			},
			attr:       createAttr(newWorkspace("root:org:ws:test").withType("root:org:foo").Workspace),
			authzError: errors.New("authorizer error"),
			wantErr:    true,
		},
		{
			name:        "ignores different resources",
			clusterName: logicalcluster.Name("root:org:ws"),
			types:       nil,
			attr: admission.NewAttributesRecord(
				&corev1alpha1.Shard{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
				},
				nil,
				corev1alpha1.Kind("Shard").WithVersion("v1alpha1"),
				"",
				"test",
				corev1alpha1.Resource("shards").WithVersion("v1alpha1"),
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
			typeLister := fakeWorkspaceTypeClusterLister(tt.types)
			o := &workspacetypeExists{
				Handler:              admission.NewHandler(admission.Create, admission.Update),
				getType:              getType(tt.types),
				typeLister:           typeLister,
				logicalClusterLister: fakeLogicalClusterClusterLister(tt.logicalClusters),
				createAuthorizer: func(clusterName logicalcluster.Name, client kcpkubernetesclientset.ClusterInterface) (authorizer.Authorizer, error) {
					return &fakeAuthorizer{
						tt.authzDecision,
						tt.authzError,
					}, nil
				},
				transitiveTypeResolver: NewTransitiveTypeResolver(typeLister.GetByPath),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: tt.clusterName})
			if err := o.Validate(ctx, tt.attr, nil); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

type fakeWorkspaceTypeClusterLister []*tenancyv1alpha1.WorkspaceType

func (f fakeWorkspaceTypeClusterLister) GetByPath(path logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error) {
	for _, t := range f {
		if canonicalPathFrom(t) == path && t.Name == name {
			return t, nil
		}
	}
	return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("workspacetypes"), name)
}

func (l fakeWorkspaceTypeClusterLister) List(selector labels.Selector) (ret []*tenancyv1alpha1.WorkspaceType, err error) {
	return l, nil
}

func (l fakeWorkspaceTypeClusterLister) Cluster(cluster logicalcluster.Name) tenancyv1alpha1listers.WorkspaceTypeLister {
	var perCluster []*tenancyv1alpha1.WorkspaceType
	for _, this := range l {
		if logicalcluster.From(this) == cluster {
			perCluster = append(perCluster, this)
		}
	}
	return fakeWorkspaceTypeLister(perCluster)
}

type fakeWorkspaceTypeLister []*tenancyv1alpha1.WorkspaceType

func (l fakeWorkspaceTypeLister) List(selector labels.Selector) (ret []*tenancyv1alpha1.WorkspaceType, err error) {
	return l, nil
}

func (l fakeWorkspaceTypeLister) Get(name string) (*tenancyv1alpha1.WorkspaceType, error) {
	for _, t := range l {
		if t.Name == name {
			return t, nil
		}
	}
	return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("workspacetype"), name)
}

type fakeLogicalClusterClusterLister []*corev1alpha1.LogicalCluster

func (l fakeLogicalClusterClusterLister) List(selector labels.Selector) (ret []*corev1alpha1.LogicalCluster, err error) {
	return l, nil
}

func (l fakeLogicalClusterClusterLister) Cluster(cluster logicalcluster.Name) corev1alpha1listers.LogicalClusterLister {
	var perCluster []*corev1alpha1.LogicalCluster
	for _, this := range l {
		if logicalcluster.From(this) == cluster {
			perCluster = append(perCluster, this)
		}
	}
	return fakeLogicalClusterLister(perCluster)
}

type fakeLogicalClusterLister []*corev1alpha1.LogicalCluster

func (l fakeLogicalClusterLister) List(selector labels.Selector) (ret []*corev1alpha1.LogicalCluster, err error) {
	return l.ListWithContext(context.Background(), selector)
}

func (l fakeLogicalClusterLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*corev1alpha1.LogicalCluster, err error) {
	return l, nil
}

func (l fakeLogicalClusterLister) Get(name string) (*corev1alpha1.LogicalCluster, error) {
	return l.GetWithContext(context.Background(), name)
}

func (l fakeLogicalClusterLister) GetWithContext(ctx context.Context, name string) (*corev1alpha1.LogicalCluster, error) {
	for _, t := range l {
		if t.Name == name {
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
		types   map[string]*tenancyv1alpha1.WorkspaceType
		input   *tenancyv1alpha1.WorkspaceType
		want    sets.String
		wantErr bool
	}{
		{
			name:  "no other types",
			input: newType("root:org:ws").WorkspaceType,
			want:  sets.NewString("root:org:ws"),
		},
		{
			name:    "error on self-reference",
			input:   newType("root:org:ws").extending("root:org:ws").WorkspaceType,
			wantErr: true,
		},
		{
			name: "extending types",
			types: map[string]*tenancyv1alpha1.WorkspaceType{
				"root:universal":    newType("root:universal").WorkspaceType,
				"root:organization": newType("root:organization").WorkspaceType,
			},
			input: newType("root:org:type").extending("root:universal").extending("root:organization").WorkspaceType,
			want:  sets.NewString("root:universal", "root:organization", "root:org:type"),
		},
		{
			name:    "missing types",
			input:   newType("root:org:type").extending("root:universal").extending("root:organization").WorkspaceType,
			want:    sets.NewString("root:universal", "root:organization", "root:org:type"),
			wantErr: true,
		},
		{
			name: "extending types transitively",
			types: map[string]*tenancyv1alpha1.WorkspaceType{
				"root:universal":    newType("root:universal").WorkspaceType,
				"root:organization": newType("root:organization").extending("root:universal").WorkspaceType,
			},
			input: newType("root:org:type").extending("root:organization").WorkspaceType,
			want:  sets.NewString("root:universal", "root:organization", "root:org:type"),
		},
		{
			name: "extending types transitively, one missing",
			types: map[string]*tenancyv1alpha1.WorkspaceType{
				"root:organization": newType("root:organization").extending("root:universal").WorkspaceType,
			},
			input:   newType("root:org:type").extending("root:organization").WorkspaceType,
			want:    sets.NewString("root:universal", "root:organization", "root:org:type"),
			wantErr: true,
		},
		{
			name: "cycle",
			types: map[string]*tenancyv1alpha1.WorkspaceType{
				"root:a": newType("root:a").extending("root:b").WorkspaceType,
				"root:b": newType("root:b").extending("root:c").WorkspaceType,
				"root:c": newType("root:c").extending("root:a").WorkspaceType,
			},
			input:   newType("root:org:type").extending("root:a").WorkspaceType,
			want:    sets.NewString("root:universal", "root:organization", "root:org:type"),
			wantErr: true,
		},
		{
			name: "cycle starting at input",
			types: map[string]*tenancyv1alpha1.WorkspaceType{
				"root:b": newType("root:b").extending("root:c").WorkspaceType,
				"root:c": newType("root:c").extending("root:a").WorkspaceType,
			},
			input:   newType("root:org:a").extending("root:b").WorkspaceType,
			want:    sets.NewString("root:universal", "root:organization", "root:org:type"),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &transitiveTypeResolver{
				getter: func(cluster logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error) {
					if t, found := tt.types[cluster.Join(name).String()]; found {
						return t, nil
					}
					return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("workspacetypes"), name)
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
					got.Insert(canonicalPathFrom(t).Join(t.Name).String())
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
		parentAliases []*tenancyv1alpha1.WorkspaceType
		childAliases  []*tenancyv1alpha1.WorkspaceType
		parentType    logicalcluster.Path
		childType     logicalcluster.Path
		wantErr       string
	}{
		{
			name:       "no child",
			childType:  logicalcluster.Path{},
			parentType: logicalcluster.Path{},
			wantErr:    "",
		},
		{
			name:       "no parents",
			childType:  logicalcluster.NewPath("root:a"),
			parentType: logicalcluster.NewPath("root:c"),
			childAliases: []*tenancyv1alpha1.WorkspaceType{
				newType("root:a").allowingParent("root:b").WorkspaceType,
			},
			wantErr: "workspace type root:a only allows [root:b] parent workspaces, but parent type root:c only implements []",
		},
		{
			name:       "no parents, any allowed parent",
			childType:  logicalcluster.NewPath("root:a"),
			parentType: logicalcluster.NewPath("root:b"),
			childAliases: []*tenancyv1alpha1.WorkspaceType{
				newType("root:a").WorkspaceType,
			},
		},
		{
			name:       "all parents allowed",
			childType:  logicalcluster.NewPath("root:a"),
			parentType: logicalcluster.NewPath("root:a"),
			parentAliases: []*tenancyv1alpha1.WorkspaceType{
				newType("root:a").WorkspaceType,
				newType("root:b").WorkspaceType,
			},
			childAliases: []*tenancyv1alpha1.WorkspaceType{
				newType("root:a").allowingParent("root:a").allowingParent("root:d").WorkspaceType,
				newType("root:b").allowingParent("root:b").allowingParent("root:d").WorkspaceType,
				newType("root:c").allowingParent("root:a").allowingParent("root:b").allowingParent("root:d").WorkspaceType,
			},
			wantErr: "",
		},
		{
			name:       "missing parent alias",
			childType:  logicalcluster.NewPath("root:a"),
			parentType: logicalcluster.NewPath("root:a"),
			parentAliases: []*tenancyv1alpha1.WorkspaceType{
				newType("root:a").WorkspaceType,
			},
			childAliases: []*tenancyv1alpha1.WorkspaceType{
				newType("root:a").allowingParent("root:a").allowingParent("root:d").WorkspaceType,
				newType("root:b").allowingParent("root:b").allowingParent("root:d").WorkspaceType,
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
			wantErr := strings.ReplaceAll(tt.wantErr, "parent", "child")
			childAliases := make([]*tenancyv1alpha1.WorkspaceType, len(tt.parentAliases))
			for i, parent := range tt.parentAliases {
				parent = parent.DeepCopy()
				parent.Spec.LimitAllowedChildren, parent.Spec.LimitAllowedParents = parent.Spec.LimitAllowedParents, parent.Spec.LimitAllowedChildren
				childAliases[i] = parent
			}
			parentAliases := make([]*tenancyv1alpha1.WorkspaceType, len(tt.childAliases))
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
		parentAliases []*tenancyv1alpha1.WorkspaceType
		childAliases  []*tenancyv1alpha1.WorkspaceType
		parentType    logicalcluster.Path
		childType     logicalcluster.Path
		wantErr       string
	}{
		{
			name:       "some type disallows children",
			childType:  logicalcluster.NewPath("root:a"),
			parentType: logicalcluster.NewPath("root:a"),
			parentAliases: []*tenancyv1alpha1.WorkspaceType{
				newType("root:a").WorkspaceType,
				newType("root:b").disallowingChildren().WorkspaceType,
			},
			childAliases: []*tenancyv1alpha1.WorkspaceType{
				newType("root:a").allowingParent("root:a").allowingParent("root:d").WorkspaceType,
				newType("root:b").allowingParent("root:b").allowingParent("root:d").WorkspaceType,
				newType("root:c").allowingParent("root:a").allowingParent("root:b").allowingParent("root:d").WorkspaceType,
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
	*tenancyv1alpha1.WorkspaceType
}

func newType(qualifiedName string) builder {
	path, name := logicalcluster.NewPath(qualifiedName).Split()
	return builder{WorkspaceType: &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey:         path.String(),
				core.LogicalClusterPathAnnotationKey: path.String(),
			},
		},
	}}
}

func (b builder) extending(qualifiedName string) builder {
	path, name := logicalcluster.NewPath(qualifiedName).Split()
	b.Spec.Extend.With = append(b.Spec.Extend.With, tenancyv1alpha1.WorkspaceTypesReference{Path: path.String(), Name: tenancyv1alpha1.WorkspaceTypesName(name)})
	return b
}

func (b builder) allowingParent(qualifiedName string) builder {
	path, name := logicalcluster.NewPath(qualifiedName).Split()
	if b.Spec.LimitAllowedParents == nil {
		b.Spec.LimitAllowedParents = &tenancyv1alpha1.WorkspaceTypeSelector{}
	}
	b.Spec.LimitAllowedParents.Types = append(b.Spec.LimitAllowedParents.Types, tenancyv1alpha1.WorkspaceTypesReference{Path: path.String(), Name: tenancyv1alpha1.WorkspaceTypesName(name)})
	return b
}

func (b builder) allowingChild(qualifiedName string) builder {
	path, name := logicalcluster.NewPath(qualifiedName).Split()
	if b.Spec.LimitAllowedChildren == nil {
		b.Spec.LimitAllowedChildren = &tenancyv1alpha1.WorkspaceTypeSelector{}
	}
	b.Spec.LimitAllowedChildren.Types = append(b.Spec.LimitAllowedChildren.Types, tenancyv1alpha1.WorkspaceTypesReference{Path: path.String(), Name: tenancyv1alpha1.WorkspaceTypesName(name)})
	return b
}

func (b builder) withDefault(qualifiedName string) builder {
	path, name := logicalcluster.NewPath(qualifiedName).Split()
	b.Spec.DefaultChildWorkspaceType = &tenancyv1alpha1.WorkspaceTypesReference{
		Path: path.String(),
		Name: tenancyv1alpha1.WorkspaceTypesName(name),
	}
	return b
}

func (b builder) disallowingChildren() builder {
	b.WorkspaceType.Spec.LimitAllowedChildren = &tenancyv1alpha1.WorkspaceTypeSelector{
		None: true,
	}
	return b
}

func (b builder) withAdditionalLabel(labels map[string]string) builder {
	b.WorkspaceType.Spec.AdditionalWorkspaceLabels = labels
	return b
}

type wsBuilder struct {
	*tenancyv1beta1.Workspace
}

func newWorkspace(qualifiedName string) wsBuilder {
	path, name := logicalcluster.NewPath(qualifiedName).Split()
	return wsBuilder{Workspace: &tenancyv1beta1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: path.String(),
			},
		},
	}}
}

func (b wsBuilder) withType(qualifiedName string) wsBuilder {
	path, name := logicalcluster.NewPath(qualifiedName).Split()
	b.Spec.Type = tenancyv1beta1.WorkspaceTypeReference{
		Path: path.String(),
		Name: tenancyv1alpha1.WorkspaceTypesName(name),
	}
	return b
}

func (b wsBuilder) withLabels(labels map[string]string) wsBuilder {
	b.Labels = labels
	return b
}

type thisWsBuilder struct {
	*corev1alpha1.LogicalCluster
}

func newLogicalCluster(clusterName string) thisWsBuilder {
	return thisWsBuilder{LogicalCluster: &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: corev1alpha1.LogicalClusterName,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: clusterName,
			},
		},
	}}
}

func (b thisWsBuilder) withType(cluster logicalcluster.Name, name string) thisWsBuilder {
	if b.Annotations == nil {
		b.Annotations = map[string]string{}
	}
	b.Annotations[corev1alpha1.LogicalClusterTypeAnnotationKey] = cluster.Path().Join(name).String()
	return b
}

func getType(types []*tenancyv1alpha1.WorkspaceType) func(path logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error) {
	return func(path logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error) {
		for _, t := range types {
			if canonicalPathFrom(t) == path && t.Name == name {
				return t, nil
			}
		}
		return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("workspacetypes"), name)
	}
}
