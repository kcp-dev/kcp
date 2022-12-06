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

package thisworkspace

import (
	"context"
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
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
)

func updateAttr(obj, old *tenancyv1alpha1.ThisWorkspace) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(obj),
		helpers.ToUnstructuredOrDie(old),
		tenancyv1alpha1.Kind("ThisWorkspace").WithVersion("v1alpha1"),
		"",
		obj.Name,
		tenancyv1alpha1.Resource("thisworkspaces").WithVersion("v1alpha1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func deleteAttr(obj *tenancyv1alpha1.ThisWorkspace, userInfo *user.DefaultInfo) admission.Attributes {
	return admission.NewAttributesRecord(
		nil,
		nil,
		tenancyv1alpha1.Kind("ThisWorkspace").WithVersion("v1alpha1"),
		"",
		obj.Name,
		tenancyv1alpha1.Resource("thisworkspaces").WithVersion("v1alpha1"),
		"",
		admission.Delete,
		&metav1.DeleteOptions{},
		false,
		userInfo,
	)
}

func TestAdmit(t *testing.T) {
	tests := []struct {
		name        string
		clusterName logicalcluster.Name
		a           admission.Attributes
		expectedObj runtime.Object
		wantErr     string
	}{
		{
			name:        "adds initializers during transition to initializing",
			clusterName: logicalcluster.New("root:org:ws"),
			a: updateAttr(
				newThisWorkspace("root:org:ws:test").withType("root:org", "foo").withInitializers("a", "b").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseInitializing,
					URL:   "https://kcp.bigcorp.com/clusters/org:test",
				}).ThisWorkspace,
				newThisWorkspace("root:org:ws:test").withType("root:org", "foo").withInitializers("a", "b").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseScheduling,
					URL:   "https://kcp.bigcorp.com/clusters/org:test",
				}).ThisWorkspace,
			),
			expectedObj: newThisWorkspace("root:org:ws:test").withType("root:org", "foo").withInitializers("a", "b").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
				Phase:        tenancyv1alpha1.WorkspacePhaseInitializing,
				URL:          "https://kcp.bigcorp.com/clusters/org:test",
				Initializers: []tenancyv1alpha1.WorkspaceInitializer{"a", "b"},
			}).ThisWorkspace,
		},
		{
			name:        "does not add initializer during transition to initializing when spec has none",
			clusterName: logicalcluster.New("root:org:ws"),
			a: updateAttr(
				newThisWorkspace("root:org:ws:test").withType("root:org", "foo").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseInitializing,
					URL:   "https://kcp.bigcorp.com/clusters/org:test",
				}).ThisWorkspace,
				newThisWorkspace("root:org:ws:test").withType("root:org", "foo").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseScheduling,
					URL:   "https://kcp.bigcorp.com/clusters/org:test",
				}).ThisWorkspace,
			),
			expectedObj: newThisWorkspace("root:org:ws:test").withType("root:org", "foo").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
				Phase: tenancyv1alpha1.WorkspacePhaseInitializing,
				URL:   "https://kcp.bigcorp.com/clusters/org:test",
			}).ThisWorkspace,
		},
		{
			name:        "does not add initializers during transition not to initializing",
			clusterName: logicalcluster.New("root:org:ws"),
			a: updateAttr(
				newThisWorkspace("root:org:ws:test").withType("root:org", "foo").withInitializers("a", "b").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseReady,
					URL:   "https://kcp.bigcorp.com/clusters/org:test",
				}).ThisWorkspace,
				newThisWorkspace("root:org:ws:test").withType("root:org", "foo").withInitializers("a", "b").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseInitializing,
					URL:   "https://kcp.bigcorp.com/clusters/org:test",
				}).ThisWorkspace,
			),
			expectedObj: newThisWorkspace("root:org:ws:test").withType("root:org", "foo").withInitializers("a", "b").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
				Phase: tenancyv1alpha1.WorkspacePhaseReady,
				URL:   "https://kcp.bigcorp.com/clusters/org:test",
			}).ThisWorkspace,
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
						"baseURL": "",
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &thisWorkspace{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: tt.clusterName})
			ctx = tenancy.WithCanonicalPath(ctx, tt.clusterName)
			if err := o.Admit(ctx, tt.a, nil); (err != nil) != (tt.wantErr != "") {
				t.Fatalf("Admit() error = %q, wantErr %q", err, tt.wantErr)
			} else if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Admit() error = %q, wantErr %q", err, tt.wantErr)
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
		thisWorkspaces []*tenancyv1alpha1.ThisWorkspace
		attr           admission.Attributes
		path           logicalcluster.Name

		wantErr string
	}{
		{
			name: "fails if spec.initializers is changed when ready",
			path: logicalcluster.New("root:org:ws"),
			attr: updateAttr(
				newThisWorkspace("root:org:ws").withInitializers("a", "b").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseReady,
				}).ThisWorkspace,
				newThisWorkspace("root:org:ws").withInitializers("a").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseReady,
				}).ThisWorkspace,
			),
			wantErr: "spec.initializers is immutable",
		},
		{
			name: "fails if spec.initializers is changed when intializing",
			path: logicalcluster.New("root:org:ws"),
			attr: updateAttr(
				newThisWorkspace("root:org:ws").withInitializers("a", "b").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseInitializing,
				}).ThisWorkspace,
				newThisWorkspace("root:org:ws").withInitializers("a").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseInitializing,
				}).ThisWorkspace,
			),
			wantErr: "spec.initializers is immutable",
		},
		{
			name: "passed if status.initializers is shrinking when initializing",
			path: logicalcluster.New("root:org:ws"),
			attr: updateAttr(
				newThisWorkspace("root:org:ws").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase:        tenancyv1alpha1.WorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{"a"},
				}).ThisWorkspace,
				newThisWorkspace("root:org:ws").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase:        tenancyv1alpha1.WorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{"a", "b"},
				}).ThisWorkspace,
			),
		},
		{
			name: "fails if status.initializers is growing",
			path: logicalcluster.New("root:org:ws"),
			attr: updateAttr(
				newThisWorkspace("root:org:ws").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase:        tenancyv1alpha1.WorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{"a", "b", "c"},
				}).ThisWorkspace,
				newThisWorkspace("root:org:ws").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase:        tenancyv1alpha1.WorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{"a", "b"},
				}).ThisWorkspace,
			),
			wantErr: "status.initializers must not grow",
		},
		{
			name: "fails if status.initializers is changing when ready",
			path: logicalcluster.New("root:org:ws"),
			attr: updateAttr(
				newThisWorkspace("root:org:ws").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase:        tenancyv1alpha1.WorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{"a"},
				}).ThisWorkspace,
				newThisWorkspace("root:org:ws").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase:        tenancyv1alpha1.WorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{"a", "b"},
				}).ThisWorkspace,
			),
			wantErr: "status.initializers is immutable after initilization",
		},
		{
			name: "spec and status initializing must match when switching to initializing",
			path: logicalcluster.New("root:org:ws"),
			attr: updateAttr(
				newThisWorkspace("root:org:ws").withInitializers("a", "b").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase:        tenancyv1alpha1.WorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{"a"},
				}).ThisWorkspace,
				newThisWorkspace("root:org:ws").withInitializers("a", "b").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseScheduling,
				}).ThisWorkspace,
			),
			wantErr: "status.initializers do not equal spec.initializers",
		},
		{
			name: "passes with equal spec and status when switching to initializing",
			path: logicalcluster.New("root:org:ws"),
			attr: updateAttr(
				newThisWorkspace("root:org:ws").withInitializers("a", "b").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase:        tenancyv1alpha1.WorkspacePhaseInitializing,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{"a", "b"},
				}).ThisWorkspace,
				newThisWorkspace("root:org:ws").withInitializers("a", "b").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseScheduling,
				}).ThisWorkspace,
			),
		},
		{
			name: "fails to move phase backwards",
			path: logicalcluster.New("root:org:ws"),
			attr: updateAttr(
				newThisWorkspace("root:org:ws").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseScheduling,
				}).ThisWorkspace,
				newThisWorkspace("root:org:ws").withStatus(tenancyv1alpha1.ThisWorkspaceStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseInitializing,
				}).ThisWorkspace,
			),
			wantErr: "cannot transition from",
		},
		{
			name:    "fails deletion as another user",
			path:    logicalcluster.New("root:org:ws"),
			attr:    deleteAttr(newThisWorkspace("root:org:ws").ThisWorkspace, &user.DefaultInfo{}),
			wantErr: "ThisWorkspace cannot be deleted",
		},
		{
			name: "passed deletion as system:masters",
			path: logicalcluster.New("root:org:ws"),
			attr: deleteAttr(
				newThisWorkspace("root:org:ws").ThisWorkspace,
				&user.DefaultInfo{Groups: []string{"system:masters"}},
			),
		},
		{
			name: "passed deletion as system:kcp:logical-cluster-admin",
			path: logicalcluster.New("root:org:ws"),
			attr: deleteAttr(
				newThisWorkspace("root:org:ws").ThisWorkspace,
				&user.DefaultInfo{Groups: []string{"system:kcp:logical-cluster-admin"}},
			),
		},
		{
			name: "passed deletion as another user if directly deletable",
			path: logicalcluster.New("root:org:ws"),
			thisWorkspaces: []*tenancyv1alpha1.ThisWorkspace{
				newThisWorkspace("root:org:ws").directlyDeletable().ThisWorkspace,
			},
			attr: deleteAttr(
				newThisWorkspace("root:org:ws").directlyDeletable().ThisWorkspace,
				&user.DefaultInfo{},
			),
		},
		{
			name: "ignores different resources",
			path: logicalcluster.New("root:org:ws"),
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
			o := &thisWorkspace{
				Handler:             admission.NewHandler(admission.Create, admission.Update, admission.Delete),
				thisWorkspaceLister: fakeThisWorkspaceClusterLister(tt.thisWorkspaces),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: tt.path})
			ctx = tenancy.WithCanonicalPath(ctx, tt.path)
			if err := o.Validate(ctx, tt.attr, nil); (err != nil) != (tt.wantErr != "") {
				t.Fatalf("Validate() error = %q, wantErr %q", err, tt.wantErr)
			} else if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, wantErr %q", err, tt.wantErr)
			}
		})
	}
}

type thisWsBuilder struct {
	*tenancyv1alpha1.ThisWorkspace
}

func newThisWorkspace(clusterName string) thisWsBuilder {
	return thisWsBuilder{ThisWorkspace: &tenancyv1alpha1.ThisWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: tenancyv1alpha1.ThisWorkspaceName,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: clusterName,
			},
		},
	}}
}

func (b thisWsBuilder) withType(cluster tenancy.Cluster, name string) thisWsBuilder {
	if b.Annotations == nil {
		b.Annotations = map[string]string{}
	}
	b.Annotations[tenancyv1alpha1.ThisWorkspaceTypeAnnotationKey] = cluster.Path().Join(name).String()
	return b
}

func (b thisWsBuilder) withInitializers(initializers ...tenancyv1alpha1.WorkspaceInitializer) thisWsBuilder {
	b.Spec.Initializers = initializers
	return b
}

func (b thisWsBuilder) directlyDeletable() thisWsBuilder {
	b.Spec.DirectlyDeletable = true
	return b
}

func (b thisWsBuilder) withStatus(status tenancyv1alpha1.ThisWorkspaceStatus) thisWsBuilder {
	b.Status = status
	return b
}

func (b thisWsBuilder) withLabels(labels map[string]string) thisWsBuilder {
	b.Labels = labels
	return b
}

type fakeThisWorkspaceClusterLister []*tenancyv1alpha1.ThisWorkspace

func (l fakeThisWorkspaceClusterLister) List(selector labels.Selector) (ret []*tenancyv1alpha1.ThisWorkspace, err error) {
	return l, nil
}

func (l fakeThisWorkspaceClusterLister) Cluster(cluster logicalcluster.Name) tenancyv1alpha1listers.ThisWorkspaceLister {
	var perCluster []*tenancyv1alpha1.ThisWorkspace
	for _, this := range l {
		if logicalcluster.From(this) == cluster {
			perCluster = append(perCluster, this)
		}
	}
	return fakeThisWorkspaceLister(perCluster)
}

type fakeThisWorkspaceLister []*tenancyv1alpha1.ThisWorkspace

func (l fakeThisWorkspaceLister) List(selector labels.Selector) (ret []*tenancyv1alpha1.ThisWorkspace, err error) {
	return l.ListWithContext(context.Background(), selector)
}

func (l fakeThisWorkspaceLister) ListWithContext(ctx context.Context, selector labels.Selector) (ret []*tenancyv1alpha1.ThisWorkspace, err error) {
	return l, nil
}

func (l fakeThisWorkspaceLister) Get(name string) (*tenancyv1alpha1.ThisWorkspace, error) {
	return l.GetWithContext(context.Background(), name)
}

func (l fakeThisWorkspaceLister) GetWithContext(ctx context.Context, name string) (*tenancyv1alpha1.ThisWorkspace, error) {
	for _, t := range l {
		if t.Name == name {
			return t, nil
		}
	}
	return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("clusterworkspace"), name)
}
