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

package logicalcluster

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v3"
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
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/core/v1alpha1"
)

func updateAttr(obj, old *corev1alpha1.LogicalCluster) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(obj),
		helpers.ToUnstructuredOrDie(old),
		tenancyv1alpha1.Kind("LogicalCluster").WithVersion("v1alpha1"),
		"",
		obj.Name,
		corev1alpha1.Resource("logicalclusters").WithVersion("v1alpha1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func deleteAttr(obj *corev1alpha1.LogicalCluster, userInfo *user.DefaultInfo) admission.Attributes {
	return admission.NewAttributesRecord(
		nil,
		nil,
		tenancyv1alpha1.Kind("LogicalCluster").WithVersion("v1alpha1"),
		"",
		obj.Name,
		corev1alpha1.Resource("logicalclusters").WithVersion("v1alpha1"),
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
			clusterName: "root:org:ws",
			a: updateAttr(
				newLogicalCluster("root:org:ws:test").withType("root:org", "foo").withInitializers("a", "b").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseInitializing,
					URL:   "https://kcp.bigcorp.com/clusters/org:test",
				}).LogicalCluster,
				newLogicalCluster("root:org:ws:test").withType("root:org", "foo").withInitializers("a", "b").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseScheduling,
					URL:   "https://kcp.bigcorp.com/clusters/org:test",
				}).LogicalCluster,
			),
			expectedObj: newLogicalCluster("root:org:ws:test").withType("root:org", "foo").withInitializers("a", "b").withStatus(corev1alpha1.LogicalClusterStatus{
				Phase:        corev1alpha1.LogicalClusterPhaseInitializing,
				URL:          "https://kcp.bigcorp.com/clusters/org:test",
				Initializers: []corev1alpha1.LogicalClusterInitializer{"a", "b"},
			}).LogicalCluster,
		},
		{
			name:        "does not add initializer during transition to initializing when spec has none",
			clusterName: "root:org:ws",
			a: updateAttr(
				newLogicalCluster("root:org:ws:test").withType("root:org", "foo").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseInitializing,
					URL:   "https://kcp.bigcorp.com/clusters/org:test",
				}).LogicalCluster,
				newLogicalCluster("root:org:ws:test").withType("root:org", "foo").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseScheduling,
					URL:   "https://kcp.bigcorp.com/clusters/org:test",
				}).LogicalCluster,
			),
			expectedObj: newLogicalCluster("root:org:ws:test").withType("root:org", "foo").withStatus(corev1alpha1.LogicalClusterStatus{
				Phase: corev1alpha1.LogicalClusterPhaseInitializing,
				URL:   "https://kcp.bigcorp.com/clusters/org:test",
			}).LogicalCluster,
		},
		{
			name:        "does not add initializers during transition not to initializing",
			clusterName: "root:org:ws",
			a: updateAttr(
				newLogicalCluster("root:org:ws:test").withType("root:org", "foo").withInitializers("a", "b").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseReady,
					URL:   "https://kcp.bigcorp.com/clusters/org:test",
				}).LogicalCluster,
				newLogicalCluster("root:org:ws:test").withType("root:org", "foo").withInitializers("a", "b").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseInitializing,
					URL:   "https://kcp.bigcorp.com/clusters/org:test",
				}).LogicalCluster,
			),
			expectedObj: newLogicalCluster("root:org:ws:test").withType("root:org", "foo").withInitializers("a", "b").withStatus(corev1alpha1.LogicalClusterStatus{
				Phase: corev1alpha1.LogicalClusterPhaseReady,
				URL:   "https://kcp.bigcorp.com/clusters/org:test",
			}).LogicalCluster,
		},
		{
			name:        "ignores different resources",
			clusterName: "root:org:ws",
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
			o := &plugin{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: tt.clusterName})
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
		name            string
		logicalClusters []*corev1alpha1.LogicalCluster
		attr            admission.Attributes
		clusterName     logicalcluster.Name

		wantErr string
	}{
		{
			name:        "fails if spec.initializers is changed when ready",
			clusterName: "root:org:ws",
			attr: updateAttr(
				newLogicalCluster("root:org:ws").withInitializers("a", "b").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseReady,
				}).LogicalCluster,
				newLogicalCluster("root:org:ws").withInitializers("a").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseReady,
				}).LogicalCluster,
			),
			wantErr: "spec.initializers is immutable",
		},
		{
			name:        "fails if spec.initializers is changed when intializing",
			clusterName: "root:org:ws",
			attr: updateAttr(
				newLogicalCluster("root:org:ws").withInitializers("a", "b").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseInitializing,
				}).LogicalCluster,
				newLogicalCluster("root:org:ws").withInitializers("a").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseInitializing,
				}).LogicalCluster,
			),
			wantErr: "spec.initializers is immutable",
		},
		{
			name:        "passed if status.initializers is shrinking when initializing",
			clusterName: "root:org:ws",
			attr: updateAttr(
				newLogicalCluster("root:org:ws").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseInitializing,
					Initializers: []corev1alpha1.LogicalClusterInitializer{"a"},
				}).LogicalCluster,
				newLogicalCluster("root:org:ws").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseInitializing,
					Initializers: []corev1alpha1.LogicalClusterInitializer{"a", "b"},
				}).LogicalCluster,
			),
		},
		{
			name:        "fails if status.initializers is growing",
			clusterName: "root:org:ws",
			attr: updateAttr(
				newLogicalCluster("root:org:ws").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseInitializing,
					Initializers: []corev1alpha1.LogicalClusterInitializer{"a", "b", "c"},
				}).LogicalCluster,
				newLogicalCluster("root:org:ws").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseInitializing,
					Initializers: []corev1alpha1.LogicalClusterInitializer{"a", "b"},
				}).LogicalCluster,
			),
			wantErr: "status.initializers must not grow",
		},
		{
			name:        "fails if status.initializers is changing when ready",
			clusterName: "root:org:ws",
			attr: updateAttr(
				newLogicalCluster("root:org:ws").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseReady,
					Initializers: []corev1alpha1.LogicalClusterInitializer{"a"},
				}).LogicalCluster,
				newLogicalCluster("root:org:ws").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseReady,
					Initializers: []corev1alpha1.LogicalClusterInitializer{"a", "b"},
				}).LogicalCluster,
			),
			wantErr: "status.initializers is immutable after initilization",
		},
		{
			name:        "spec and status initializing must match when switching to initializing",
			clusterName: "root:org:ws",
			attr: updateAttr(
				newLogicalCluster("root:org:ws").withInitializers("a", "b").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseInitializing,
					Initializers: []corev1alpha1.LogicalClusterInitializer{"a"},
				}).LogicalCluster,
				newLogicalCluster("root:org:ws").withInitializers("a", "b").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseScheduling,
				}).LogicalCluster,
			),
			wantErr: "status.initializers do not equal spec.initializers",
		},
		{
			name:        "passes with equal spec and status when switching to initializing",
			clusterName: "root:org:ws",
			attr: updateAttr(
				newLogicalCluster("root:org:ws").withInitializers("a", "b").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseInitializing,
					Initializers: []corev1alpha1.LogicalClusterInitializer{"a", "b"},
				}).LogicalCluster,
				newLogicalCluster("root:org:ws").withInitializers("a", "b").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseScheduling,
				}).LogicalCluster,
			),
		},
		{
			name:        "fails to move phase backwards",
			clusterName: "root:org:ws",
			attr: updateAttr(
				newLogicalCluster("root:org:ws").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseScheduling,
				}).LogicalCluster,
				newLogicalCluster("root:org:ws").withStatus(corev1alpha1.LogicalClusterStatus{
					Phase: corev1alpha1.LogicalClusterPhaseInitializing,
				}).LogicalCluster,
			),
			wantErr: "cannot transition from",
		},
		{
			name:        "fails deletion as another user",
			clusterName: "root:org:ws",
			attr:        deleteAttr(newLogicalCluster("root:org:ws").LogicalCluster, &user.DefaultInfo{}),
			wantErr:     "LogicalCluster cannot be deleted",
		},
		{
			name:        "passed deletion as system:masters",
			clusterName: "root:org:ws",
			attr: deleteAttr(
				newLogicalCluster("root:org:ws").LogicalCluster,
				&user.DefaultInfo{Groups: []string{"system:masters"}},
			),
		},
		{
			name:        "passed deletion as system:kcp:logical-cluster-admin",
			clusterName: "root:org:ws",
			attr: deleteAttr(
				newLogicalCluster("root:org:ws").LogicalCluster,
				&user.DefaultInfo{Groups: []string{"system:kcp:logical-cluster-admin"}},
			),
		},
		{
			name:        "passed deletion as another user if directly deletable",
			clusterName: "root:org:ws",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster("root:org:ws").directlyDeletable().LogicalCluster,
			},
			attr: deleteAttr(
				newLogicalCluster("root:org:ws").directlyDeletable().LogicalCluster,
				&user.DefaultInfo{},
			),
		},
		{
			name:        "ignores different resources",
			clusterName: "root:org:ws",
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
			o := &plugin{
				Handler:              admission.NewHandler(admission.Create, admission.Update, admission.Delete),
				logicalClusterLister: fakeLogicalClusterClusterLister(tt.logicalClusters),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: tt.clusterName})
			if err := o.Validate(ctx, tt.attr, nil); (err != nil) != (tt.wantErr != "") {
				t.Fatalf("Validate() error = %q, wantErr %q", err, tt.wantErr)
			} else if tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, wantErr %q", err, tt.wantErr)
			}
		})
	}
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

func (b thisWsBuilder) withInitializers(initializers ...corev1alpha1.LogicalClusterInitializer) thisWsBuilder {
	b.Spec.Initializers = initializers
	return b
}

func (b thisWsBuilder) directlyDeletable() thisWsBuilder {
	b.Spec.DirectlyDeletable = true
	return b
}

func (b thisWsBuilder) withStatus(status corev1alpha1.LogicalClusterStatus) thisWsBuilder {
	b.Status = status
	return b
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
