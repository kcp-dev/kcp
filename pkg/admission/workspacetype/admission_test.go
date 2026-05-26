/*
Copyright 2026 The kcp Authors.

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

package workspacetype

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
)

func toUnstructured(t *testing.T, obj runtime.Object) *unstructured.Unstructured {
	t.Helper()
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatalf("failed to convert to unstructured: %v", err)
	}
	return &unstructured.Unstructured{Object: raw}
}

func makeAttr(t *testing.T, op admission.Operation, newWT, oldWT *tenancyv1alpha1.WorkspaceType, userInfo user.Info) admission.Attributes {
	t.Helper()
	var oldObj runtime.Object
	if oldWT != nil {
		oldObj = toUnstructured(t, oldWT)
	}
	if userInfo == nil {
		userInfo = &user.DefaultInfo{Name: "alice"}
	}
	return admission.NewAttributesRecord(
		toUnstructured(t, newWT),
		oldObj,
		tenancyv1alpha1.Kind("WorkspaceType").WithVersion("v1alpha1"),
		"",
		newWT.Name,
		tenancyv1alpha1.Resource("workspacetypes").WithVersion("v1alpha1"),
		"",
		op,
		&metav1.CreateOptions{},
		false,
		userInfo,
	)
}

func newWorkspaceType(cluster logicalcluster.Name, name string, refs ...tenancyv1alpha1.APIExportReference) *tenancyv1alpha1.WorkspaceType {
	wt := &tenancyv1alpha1.WorkspaceType{
		TypeMeta: metav1.TypeMeta{
			APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(),
			Kind:       "WorkspaceType",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
		},
	}
	if len(refs) > 0 {
		wt.Spec.DefaultAPIBindings = refs
	}
	return wt
}

type fakeAuthorizer struct {
	decision authorizer.Decision
	err      error
}

func (a *fakeAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	return a.decision, "", a.err
}

// newPlugin creates a workspacetype admission plugin where knownPaths maps a
// workspace path string (e.g. "root:some:path") to the cluster name it
// resolves to. Lookups for paths absent from the map return NotFound,
// simulating an unreachable workspace.
func newPlugin(decision authorizer.Decision, knownPaths map[string]logicalcluster.Name) (*workspacetype, *logicalcluster.Name) {
	var capturedCluster logicalcluster.Name
	p := &workspacetype{
		Handler: admission.NewHandler(admission.Create, admission.Update),
		createAuthorizer: func(clusterName logicalcluster.Name, client kcpkubernetesclientset.ClusterInterface, opts delegated.Options) (authorizer.Authorizer, error) {
			capturedCluster = clusterName
			return &fakeAuthorizer{decision: decision}, nil
		},
		getLogicalCluster: func(path logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
			cluster, ok := knownPaths[path.String()]
			if !ok {
				return nil, apierrors.NewNotFound(corev1alpha1.Resource("logicalclusters"), path.String())
			}
			return &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:        corev1alpha1.LogicalClusterName,
					Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
				},
			}, nil
		},
	}
	p.SetReadyFunc(func() bool { return true })
	return p, &capturedCluster
}

func TestValidate_DefaultAPIBindings(t *testing.T) {
	tenantCluster := logicalcluster.Name("root-test")

	tests := []struct {
		name                  string
		op                    admission.Operation
		newWT                 *tenancyv1alpha1.WorkspaceType
		oldWT                 *tenancyv1alpha1.WorkspaceType
		decision              authorizer.Decision
		knownPaths            map[string]logicalcluster.Name
		wantForbid            bool
		wantContains          string
		wantAuthorizerCluster logicalcluster.Name
	}{
		{
			name:  "create without defaultAPIBindings is allowed",
			op:    admission.Create,
			newWT: newWorkspaceType(tenantCluster, "ok"),
		},
		{
			name: "create with defaultAPIBindings allowed when user has bind",
			op:   admission.Create,
			newWT: newWorkspaceType(tenantCluster, "good",
				tenancyv1alpha1.APIExportReference{Path: "root", Export: "cowboys"},
			),
			decision: authorizer.DecisionAllow,
		},
		{
			name: "create with defaultAPIBindings denied when user lacks bind",
			op:   admission.Create,
			newWT: newWorkspaceType(tenantCluster, "bad",
				tenancyv1alpha1.APIExportReference{Path: "root", Export: "cowboys"},
			),
			decision:     authorizer.DecisionDeny,
			wantForbid:   true,
			wantContains: "no permission to bind to export root:cowboys",
		},
		{
			name:  "update adding new defaultAPIBinding requires bind on new entry",
			op:    admission.Update,
			oldWT: newWorkspaceType(tenantCluster, "x"),
			newWT: newWorkspaceType(tenantCluster, "x",
				tenancyv1alpha1.APIExportReference{Path: "root", Export: "cowboys"},
			),
			decision:   authorizer.DecisionDeny,
			wantForbid: true,
		},
		{
			name: "update keeping existing defaultAPIBinding does not re-check",
			op:   admission.Update,
			oldWT: newWorkspaceType(tenantCluster, "x",
				tenancyv1alpha1.APIExportReference{Path: "root", Export: "cowboys"},
			),
			newWT: newWorkspaceType(tenantCluster, "x",
				tenancyv1alpha1.APIExportReference{Path: "root", Export: "cowboys"},
			),
			// Even with deny, this should pass because the entry is preexisting.
			decision: authorizer.DecisionDeny,
		},
		{
			name: "create with empty path resolves to current cluster",
			op:   admission.Create,
			newWT: newWorkspaceType(tenantCluster, "y",
				tenancyv1alpha1.APIExportReference{Export: "local"},
			),
			decision: authorizer.DecisionAllow,
		},
		{
			name: "create denied when referenced workspace cannot be resolved",
			op:   admission.Create,
			newWT: newWorkspaceType(tenantCluster, "z",
				tenancyv1alpha1.APIExportReference{Path: "some:other:cluster", Export: "missing"},
			),
			// Even with Allow, denied. The error must be byte-identical to
			// the bind-denied case so admission cannot be used as a
			// workspace-existence oracle by a caller with WST create perms.
			decision:     authorizer.DecisionAllow,
			wantForbid:   true,
			wantContains: "no permission to bind to export some:other:cluster:missing",
		},
		{
			name: "create with non-root path uses cluster from resolved LogicalCluster",
			op:   admission.Create,
			newWT: newWorkspaceType(tenantCluster, "remote",
				tenancyv1alpha1.APIExportReference{Path: "some:other:path", Export: "cowboys-remote"},
			),
			decision: authorizer.DecisionAllow,
			knownPaths: map[string]logicalcluster.Name{
				"some:other:path": logicalcluster.Name("actual-export-cluster"),
			},
			wantAuthorizerCluster: logicalcluster.Name("actual-export-cluster"),
		},
		{
			name: "create allowed for forward reference to APIExport when workspace exists and user has bind",
			op:   admission.Create,
			newWT: newWorkspaceType(tenantCluster, "forward",
				tenancyv1alpha1.APIExportReference{Path: "root:provider", Export: "future-export"},
			),
			// APIExport doesn't exist yet, but the workspace at root:provider
			// does and the user holds bind on apiexports/future-export
			// (resourceNames in RBAC match nonexistent named resources).
			decision: authorizer.DecisionAllow,
			knownPaths: map[string]logicalcluster.Name{
				"root:provider": logicalcluster.Name("provider-cluster"),
			},
			wantAuthorizerCluster: logicalcluster.Name("provider-cluster"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, capturedCluster := newPlugin(tt.decision, tt.knownPaths)
			attr := makeAttr(t, tt.op, tt.newWT, tt.oldWT, nil)
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: tenantCluster})
			err := p.Validate(ctx, attr, nil)
			if tt.wantForbid {
				if err == nil {
					t.Fatal("expected forbidden error, got nil")
				}
				if tt.wantContains != "" && !strings.Contains(err.Error(), tt.wantContains) {
					t.Fatalf("expected error to contain %q, got %q", tt.wantContains, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantAuthorizerCluster != "" && *capturedCluster != tt.wantAuthorizerCluster {
				t.Fatalf("expected authorizer cluster %q, got %q", tt.wantAuthorizerCluster, *capturedCluster)
			}
		})
	}
}
