/*
Copyright 2022 The kcp Authors.

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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	"github.com/kcp-dev/kcp/pkg/authorization"
)

func createAttr(ws *tenancyv1alpha1.Workspace) admission.Attributes {
	return createAttrWithUser(ws, &kuser.DefaultInfo{})
}

func createAttrWithUser(ws *tenancyv1alpha1.Workspace, info kuser.Info) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(ws),
		nil,
		tenancyv1alpha1.Kind("Workspace").WithVersion("v1alpha1"),
		"",
		ws.Name,
		tenancyv1alpha1.Resource("workspaces").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		info,
	)
}

func updateAttr(ws, old *tenancyv1alpha1.Workspace) admission.Attributes {
	return updateAttrWithUser(ws, old, &kuser.DefaultInfo{})
}

func updateAttrWithUser(ws, old *tenancyv1alpha1.Workspace, info kuser.Info) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(ws),
		helpers.ToUnstructuredOrDie(old),
		tenancyv1alpha1.Kind("Workspace").WithVersion("v1alpha1"),
		"",
		ws.Name,
		tenancyv1alpha1.Resource("workspaces").WithVersion("v1alpha1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		info,
	)
}

func TestAdmit(t *testing.T) {
	t.Parallel()
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
			name: "adds user information on create",
			types: []*tenancyv1alpha1.WorkspaceType{
				newType("root:org:foo").WorkspaceType,
			},
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org:ws")).LogicalCluster,
			},
			clusterName: "root:org:ws",
			a: createAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
			}, &kuser.DefaultInfo{
				Name:   "someone",
				UID:    "id",
				Groups: []string{"a", "b"},
				Extra: map[string][]string{
					"one": {"1", "01"},
				},
			}),
			expectedObj: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.io/owner": `{"username":"someone","uid":"id","groups":["a","b"],"extra":{"one":["1","01"]}}`,
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
			},
		},
		{
			name: "keep user information on create when privileged system user",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org:ws")).LogicalCluster,
			},
			clusterName: "root:org:ws",
			a: createAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.io/owner": `{"username":"someoneelse","uid":"otherid","groups":["c","d"],"extra":{"two":["2","02"]}}`,
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}, &kuser.DefaultInfo{
				Name:   "someone",
				UID:    "id",
				Groups: []string{"a", "b", kuser.SystemPrivilegedGroup},
				Extra: map[string][]string{
					"one": {"1", "01"},
				},
			}),
			expectedObj: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.io/owner": `{"username":"someoneelse","uid":"otherid","groups":["c","d"],"extra":{"two":["2","02"]}}`,
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			},
		},
		{
			name: "override user information on create when not privileged system user",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org:ws")).LogicalCluster,
			},
			clusterName: "root:org:ws",
			a: createAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.io/owner": `{"username":"someoneelse","uid":"otherid","groups":["c","d"],"extra":{"two":["2","02"]}}`,
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}, &kuser.DefaultInfo{
				Name:   "someone",
				UID:    "id",
				Groups: []string{"a", "b"},
				Extra: map[string][]string{
					"one": {"1", "01"},
				},
			}),
			expectedObj: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.io/owner": `{"username":"someone","uid":"id","groups":["a","b"],"extra":{"one":["1","01"]}}`,
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			},
		},
		{
			name: "copies required groups on create",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org:ws")).WithRequiredGroups("foo", "bar").LogicalCluster,
			},
			clusterName: "root:org:ws",
			a: createAttr(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{},
			}),
			expectedObj: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"authorization.kcp.io/required-groups": "foo,bar",
						"experimental.tenancy.kcp.io/owner":    "{}",
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{},
			},
		},
		{
			name: "replaces required groups on create as non-system:master",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org:ws")).WithRequiredGroups("foo", "bar").LogicalCluster,
			},
			clusterName: "root:org:ws",
			a: createAttr(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"authorization.kcp.io/required-groups": "foo,abc",
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{},
			}),
			expectedObj: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"authorization.kcp.io/required-groups": "foo,bar",
						"experimental.tenancy.kcp.io/owner":    "{}",
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{},
			},
		},
		{
			name: "keeps required groups on create as system:master",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org:ws")).WithRequiredGroups("foo", "bar").LogicalCluster,
			},
			clusterName: "root:org:ws",
			a: createAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"authorization.kcp.io/required-groups": "foo,abc",
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{},
			}, &kuser.DefaultInfo{
				Name:   "admin",
				Groups: []string{kuser.SystemPrivilegedGroup},
			}),
			expectedObj: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"authorization.kcp.io/required-groups": "foo,abc",
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &workspace{
				Handler:              admission.NewHandler(admission.Create, admission.Update),
				logicalClusterLister: fakeLogicalClusterClusterLister(tt.logicalClusters),
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
	t.Parallel()
	tests := []struct {
		name            string
		logicalClusters []*corev1alpha1.LogicalCluster
		a               admission.Attributes
		expectedErrors  []string
	}{
		{
			name: "rejects type mutations",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).LogicalCluster,
			},
			a: updateAttr(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
			},
				&tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test",
						Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
					},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						Type: &tenancyv1alpha1.WorkspaceTypeReference{
							Name: "universal",
							Path: "root:org",
						},
					},
				}),
			expectedErrors: []string{"field is immutable"},
		},
		{
			name: "rejects unsetting cluster",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).LogicalCluster,
			},
			a: updateAttr(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{}},
				&tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test",
						Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
					},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						Cluster: "somewhere",
						Type: &tenancyv1alpha1.WorkspaceTypeReference{
							Name: "foo",
							Path: "root:org",
						},
					},
				}),
			expectedErrors: []string{"spec.cluster cannot be unset"},
		},
		{
			name: "allows transition to ready directly when valid",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).LogicalCluster,
			},
			a: updateAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Cluster: "somewhere",
					URL:     "https://kcp.bigcorp.com/clusters/org:test",
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseReady,
					Initializers: []corev1alpha1.LogicalClusterInitializer{},
				},
			},
				&tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test",
						Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
					},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						Type: &tenancyv1alpha1.WorkspaceTypeReference{
							Name: "foo",
							Path: "root:org",
						},
					},
					Status: tenancyv1alpha1.WorkspaceStatus{
						Phase:        corev1alpha1.LogicalClusterPhaseScheduling,
						Initializers: []corev1alpha1.LogicalClusterInitializer{"a"},
					},
				}, &kuser.DefaultInfo{Groups: []string{kuser.SystemPrivilegedGroup}}),
		},
		{
			name: "allows creation to ready directly when valid",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).LogicalCluster,
			},
			a: createAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Cluster: "somewhere",
					URL:     "https://kcp.bigcorp.com/clusters/org:test",
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseReady,
					Initializers: []corev1alpha1.LogicalClusterInitializer{},
				},
			}, &kuser.DefaultInfo{Groups: []string{kuser.SystemPrivilegedGroup}}),
		},
		{
			name: "rejects creation with spec.cluster when unprivileged user",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).LogicalCluster,
			},
			a: createAttr(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Cluster: "somewhere",
					URL:     "https://kcp.bigcorp.com/clusters/org:test",
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseReady,
					Initializers: []corev1alpha1.LogicalClusterInitializer{},
				},
			}),
			expectedErrors: []string{"spec.Cluster can only be set by system privileged users"},
		},
		{
			name: "rejects changing url from unprivileged users",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).LogicalCluster,
			},
			a: updateAttr(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Cluster: "somewhere",
					URL:     "https://kcp.bigcorp.com/clusters/org:test",
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseReady,
					Initializers: []corev1alpha1.LogicalClusterInitializer{},
				},
			},
				&tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test",
						Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
					},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						Cluster: "somewhere",
						URL:     "https://kcp.otherbigcorp.com/clusters/org:test",
						Type: &tenancyv1alpha1.WorkspaceTypeReference{
							Name: "foo",
							Path: "root:org",
						},
					},
					Status: tenancyv1alpha1.WorkspaceStatus{
						Phase:        corev1alpha1.LogicalClusterPhaseScheduling,
						Initializers: []corev1alpha1.LogicalClusterInitializer{"a"},
					},
				}),
			expectedErrors: []string{"spec.URL can only be changed by system privileged users"},
		},
		{
			name: "rejects transition to ready directly when invalid",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).LogicalCluster,
			},
			a: updateAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Cluster: "somewhere",
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase:        corev1alpha1.LogicalClusterPhaseReady,
					Initializers: []corev1alpha1.LogicalClusterInitializer{},
				},
			},
				&tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test",
						Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
					},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						Type: &tenancyv1alpha1.WorkspaceTypeReference{
							Name: "foo",
							Path: "root:org",
						},
					},
					Status: tenancyv1alpha1.WorkspaceStatus{
						Phase:        corev1alpha1.LogicalClusterPhaseScheduling,
						Initializers: []corev1alpha1.LogicalClusterInitializer{"a"},
					},
				}, &kuser.DefaultInfo{Groups: []string{kuser.SystemPrivilegedGroup}}),
			expectedErrors: []string{"spec.URL must be set for phase Ready"},
		},
		{
			name: "mounted: rejects spec.URL change by non-privileged users",
			a: updateAttr(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "mount",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "https://attacker.example.com",
					Cluster: "",
					Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
			},
				&tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "mount",
						Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
					},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						URL:     "https://kcp.bigcorp.com/clusters/org:dest",
						Cluster: "",
						Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
					},
					Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
				}),
			expectedErrors: []string{"spec.URL can only be changed by system privileged users"},
		},
		{
			name: "mounted: rejects spec.cluster change by non-privileged users",
			a: updateAttr(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "mount",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "https://kcp.bigcorp.com/clusters/org:dest",
					Cluster: "somewhere",
					Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
			},
				&tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "mount",
						Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
					},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						URL:     "https://kcp.bigcorp.com/clusters/org:dest",
						Cluster: "",
						Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
					},
					Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
				}),
			expectedErrors: []string{"spec.cluster can only be changed by system privileged users"},
		},
		{
			name: "mounted: rejects spec.cluster even for privileged users",
			a: updateAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "mount",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "https://kcp.bigcorp.com/clusters/org:dest",
					Cluster: "somewhere",
					Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
			},
				&tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "mount",
						Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
					},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						URL:     "https://kcp.bigcorp.com/clusters/org:dest",
						Cluster: "",
						Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
					},
					Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
				}, &kuser.DefaultInfo{Groups: []string{kuser.SystemPrivilegedGroup}}),
			expectedErrors: []string{"spec.cluster cannot be set for mounted workspaces"},
		},
		{
			name: "mounted: rejects http spec.URL even for privileged users",
			a: updateAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "mount",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "http://kcp.bigcorp.com/clusters/org:dest",
					Cluster: "",
					Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
			},
				&tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "mount",
						Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
					},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						URL:     "",
						Cluster: "",
						Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
					},
					Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseUnavailable},
				}, &kuser.DefaultInfo{Groups: []string{kuser.SystemPrivilegedGroup}}),
			expectedErrors: []string{"spec.URL is not a valid mount target", "scheme must be https"},
		},
		{
			name: "mounted: rejects spec.URL with user info even for privileged users",
			a: updateAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "mount",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "https://user:pass@kcp.bigcorp.com/clusters/org:dest",
					Cluster: "",
					Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
			},
				&tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "mount",
						Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
					},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						URL:     "",
						Cluster: "",
						Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
					},
					Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseUnavailable},
				}, &kuser.DefaultInfo{Groups: []string{kuser.SystemPrivilegedGroup}}),
			expectedErrors: []string{"spec.URL is not a valid mount target", "must not contain user info"},
		},
		{
			name: "mounted: rejects transition to Ready without spec.URL",
			a: updateAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "mount",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "",
					Cluster: "",
					Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
			},
				&tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "mount",
						Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
					},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						URL:     "",
						Cluster: "",
						Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
					},
					Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseUnavailable},
				}, &kuser.DefaultInfo{Groups: []string{kuser.SystemPrivilegedGroup}}),
			expectedErrors: []string{"spec.URL must be set for phase Ready"},
		},
		{
			name: "mounted: rejects unsetting spec.mount",
			a: updateAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "mount",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{URL: "https://kcp.bigcorp.com/clusters/org:dest"},
			},
				&tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "mount",
						Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
					},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						URL:     "https://kcp.bigcorp.com/clusters/org:dest",
						Cluster: "",
						Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
					},
					Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
				}, &kuser.DefaultInfo{Groups: []string{kuser.SystemPrivilegedGroup}}),
			expectedErrors: []string{"spec.mount cannot be unset"},
		},
		{
			name: "mounted: allows privileged users to set a valid https spec.URL and go Ready",
			a: updateAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "mount",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "https://kcp.bigcorp.com/clusters/org:dest",
					Cluster: "",
					Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseReady},
			},
				&tenancyv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "mount",
						Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
					},
					Spec: tenancyv1alpha1.WorkspaceSpec{
						URL:     "",
						Cluster: "",
						Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
					},
					Status: tenancyv1alpha1.WorkspaceStatus{Phase: corev1alpha1.LogicalClusterPhaseUnavailable},
				}, &kuser.DefaultInfo{Groups: []string{kuser.SystemPrivilegedGroup}}),
		},
		{
			name: "mounted: rejects http spec.URL on create even for privileged users",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).LogicalCluster,
			},
			a: createAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "mount",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					URL:     "http://kcp.bigcorp.com/clusters/org:dest",
					Cluster: "",
					Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{Phase: ""},
			}, &kuser.DefaultInfo{Groups: []string{kuser.SystemPrivilegedGroup}}),
			expectedErrors: []string{"spec.URL is not a valid mount target"},
		},
		{
			name: "ignores different resources",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).LogicalCluster,
			},
			a: admission.NewAttributesRecord(
				&corev1alpha1.Shard{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "test",
						Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
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
				&kuser.DefaultInfo{},
			),
		},
		{
			name: "checks user information on create",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).LogicalCluster,
			},
			a: createAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test",
					Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "foo",
						Path: "root:org",
					},
				},
			}, &kuser.DefaultInfo{
				Name:   "someone",
				UID:    "id",
				Groups: []string{"a", "b"},
				Extra: map[string][]string{
					"one": {"1", "01"},
				},
			}),
			expectedErrors: []string{"expected user annotation experimental.tenancy.kcp.io/owner={\"username\":\"someone\",\"uid\":\"id\",\"groups\":[\"a\",\"b\"],\"extra\":{\"one\":[\"1\",\"01\"]}}"},
		},
		{
			name: "accept user information on create when privileged system user",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).LogicalCluster,
			},
			a: createAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.io/owner": `{"username":"someoneelse","uid":"otherid","groups":["c","d"],"extra":{"two":["2","02"]}}`,
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
				Status: tenancyv1alpha1.WorkspaceStatus{
					Phase: corev1alpha1.LogicalClusterPhaseScheduling,
				},
			}, &kuser.DefaultInfo{
				Name:   "someone",
				UID:    "id",
				Groups: []string{"a", "b", kuser.SystemPrivilegedGroup},
				Extra: map[string][]string{
					"one": {"1", "01"},
				},
			}),
		},
		{
			name: "reject wrong user information on create when not privileged system user",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).LogicalCluster,
			},
			a: createAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.io/owner": `{"username":"someoneelse","uid":"otherid","groups":["c","d"],"extra":{"two":["2","02"]}}`,
					},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Type: &tenancyv1alpha1.WorkspaceTypeReference{
						Name: "Foo",
						Path: "root:org",
					},
				},
			}, &kuser.DefaultInfo{
				Name:   "someone",
				UID:    "id",
				Groups: []string{"a", "b"},
				Extra: map[string][]string{
					"one": {"1", "01"},
				},
			}),
			expectedErrors: []string{"expected user annotation experimental.tenancy.kcp.io/owner={\"username\":\"someone\",\"uid\":\"id\",\"groups\":[\"a\",\"b\"],\"extra\":{\"one\":[\"1\",\"01\"]}}"},
		},
		{
			name: "rejects with wrong required groups on create as non-system:master",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).WithRequiredGroups("foo", "bar").LogicalCluster,
			},
			a: createAttr(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.io/owner": "{}",
					},
				},
			}),
			expectedErrors: []string{"missing required groups annotation authorization.kcp.io/required-groups=foo,bar"},
		},
		{
			name: "accepts with equal required groups on create",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).WithRequiredGroups("foo", "bar").LogicalCluster,
			},
			a: createAttr(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"authorization.kcp.io/required-groups": "foo,bar",
						"experimental.tenancy.kcp.io/owner":    "{}",
					},
				},
			}),
		},
		{
			name: "accepts with wrong required groups on create as system:master",
			logicalClusters: []*corev1alpha1.LogicalCluster{
				newLogicalCluster(logicalcluster.NewPath("root:org")).WithRequiredGroups("foo", "bar").LogicalCluster,
			},
			a: createAttrWithUser(&tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.io/owner": "{}",
					},
				},
			}, &kuser.DefaultInfo{
				Name:   "admin",
				Groups: []string{kuser.SystemPrivilegedGroup},
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &workspace{
				Handler:              admission.NewHandler(admission.Create, admission.Update),
				logicalClusterLister: fakeLogicalClusterClusterLister(tt.logicalClusters),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: "root:org"})
			err := o.Validate(ctx, tt.a, nil)
			t.Logf("%v", err)
			wantErr := len(tt.expectedErrors) > 0
			require.Equal(t, wantErr, err != nil, "expected error: %v, got: %v", tt.expectedErrors, err)

			if err != nil {
				t.Logf("Got admission errors: %v", err)
				for _, expected := range tt.expectedErrors {
					require.Contains(t, err.Error(), expected)
				}
			}
		})
	}
}

// TestValidateGrandfatheredMount covers a workspace whose spec.URL was written
// before mount targets had to be acceptable URLs, e.g. by an older kcp with the
// WorkspaceMounts gate on. Such a value must not make the object unwritable:
// its own controller still has to update status, and an operator still has to be
// able to fix or remove it. Routing safety does not depend on this check, since
// the index and the mount proxy refuse to route a target that is not https.
func TestValidateGrandfatheredMount(t *testing.T) {
	t.Parallel()

	const (
		legacyURL = "http://legacy.example.com/ingest"
		validURL  = "https://good.example.com/clusters/root:dest"
	)
	mounted := func(url, cluster string, phase corev1alpha1.LogicalClusterPhaseType, labels map[string]string) *tenancyv1alpha1.Workspace {
		return &tenancyv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "mount",
				Labels:      labels,
				Annotations: map[string]string{"experimental.tenancy.kcp.io/owner": "{}"},
			},
			Spec: tenancyv1alpha1.WorkspaceSpec{
				URL:     url,
				Cluster: cluster,
				Mount:   &tenancyv1alpha1.Mount{Reference: tenancyv1alpha1.ObjectReference{APIVersion: "contrib.kcp.io/v1alpha1", Kind: "KubeCluster", Name: "proxy-cluster"}},
			},
			Status: tenancyv1alpha1.WorkspaceStatus{Phase: phase},
		}
	}
	priv := &kuser.DefaultInfo{Groups: []string{kuser.SystemPrivilegedGroup}}

	tests := []struct {
		name           string
		a              admission.Attributes
		expectedErrors []string
	}{
		{
			// What the mounts controller does when it reports the mount as no
			// longer usable. If this is rejected the controller cannot publish
			// the condition and retries forever.
			name: "allows a status-only update",
			a: updateAttrWithUser(
				mounted(legacyURL, "", corev1alpha1.LogicalClusterPhaseUnavailable, nil),
				mounted(legacyURL, "", corev1alpha1.LogicalClusterPhaseReady, nil), priv),
		},
		{
			name: "allows a metadata-only update",
			a: updateAttrWithUser(
				mounted(legacyURL, "", corev1alpha1.LogicalClusterPhaseReady, map[string]string{"a": "b"}),
				mounted(legacyURL, "", corev1alpha1.LogicalClusterPhaseReady, nil), priv),
		},
		{
			name: "allows replacing it with a valid URL",
			a: updateAttrWithUser(
				mounted(validURL, "", corev1alpha1.LogicalClusterPhaseReady, nil),
				mounted(legacyURL, "", corev1alpha1.LogicalClusterPhaseReady, nil), priv),
		},
		{
			name: "allows clearing it",
			a: updateAttrWithUser(
				mounted("", "", corev1alpha1.LogicalClusterPhaseUnavailable, nil),
				mounted(legacyURL, "", corev1alpha1.LogicalClusterPhaseReady, nil), priv),
		},
		{
			// A grandfathered spec.cluster must be clearable too: a mount has no
			// logical cluster, so clearing it is the remediation, not an error.
			name: "allows clearing a grandfathered spec.cluster",
			a: updateAttrWithUser(
				mounted(validURL, "", corev1alpha1.LogicalClusterPhaseReady, nil),
				mounted(validURL, "somewhere", corev1alpha1.LogicalClusterPhaseReady, nil), priv),
		},
		{
			// Keeping a grandfathered value is tolerated; writing a new bad one
			// is not, whoever does it.
			name: "still rejects changing it to another unacceptable URL",
			a: updateAttrWithUser(
				mounted("http://attacker.example.com", "", corev1alpha1.LogicalClusterPhaseReady, nil),
				mounted(legacyURL, "", corev1alpha1.LogicalClusterPhaseReady, nil), priv),
			expectedErrors: []string{"spec.URL is not a valid mount target", "scheme must be https"},
		},
		{
			name: "still rejects a tenant changing it",
			a: updateAttr(
				mounted(validURL, "", corev1alpha1.LogicalClusterPhaseReady, nil),
				mounted(legacyURL, "", corev1alpha1.LogicalClusterPhaseReady, nil)),
			expectedErrors: []string{"spec.URL can only be changed by system privileged users"},
		},
		{
			name:           "still rejects an unacceptable URL on create",
			a:              createAttrWithUser(mounted(legacyURL, "", "", nil), priv),
			expectedErrors: []string{"spec.URL is not a valid mount target"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			o := &workspace{
				Handler: admission.NewHandler(admission.Create, admission.Update),
				logicalClusterLister: fakeLogicalClusterClusterLister{
					newLogicalCluster(logicalcluster.NewPath("root:org")).LogicalCluster,
				},
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: "root:org"})
			err := o.Validate(ctx, tt.a, nil)
			t.Logf("%v", err)
			require.Equal(t, len(tt.expectedErrors) > 0, err != nil, "expected errors %v, got: %v", tt.expectedErrors, err)
			for _, expected := range tt.expectedErrors {
				require.ErrorContains(t, err, expected)
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
				logicalcluster.AnnotationKey: path.String(),
			},
		},
	}}
}

type thisBuilder struct {
	*corev1alpha1.LogicalCluster
}

func newLogicalCluster(clusterName logicalcluster.Path) thisBuilder {
	return thisBuilder{LogicalCluster: &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name: corev1alpha1.LogicalClusterName,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: clusterName.String(),
			},
		},
	}}
}

func (b thisBuilder) WithRequiredGroups(groups ...string) thisBuilder {
	if len(groups) > 0 {
		b.LogicalCluster.Annotations[authorization.RequiredGroupsAnnotationKey] = strings.Join(groups, ",")
	}
	return b
}

type fakeLogicalClusterClusterLister []*corev1alpha1.LogicalCluster

func (l fakeLogicalClusterClusterLister) List(selector labels.Selector) (ret []*corev1alpha1.LogicalCluster, err error) {
	return l, nil
}

func (l fakeLogicalClusterClusterLister) Cluster(cluster logicalcluster.Name) corev1alpha1listers.LogicalClusterLister {
	var perCluster []*corev1alpha1.LogicalCluster
	for _, logicalCluster := range l {
		if logicalcluster.From(logicalCluster) == cluster {
			perCluster = append(perCluster, logicalCluster)
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
	return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("workspace"), name)
}
