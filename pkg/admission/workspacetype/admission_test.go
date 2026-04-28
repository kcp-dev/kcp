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
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
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

func newPlugin(decision authorizer.Decision, exports ...*apisv1alpha2.APIExport) *workspacetype {
	exportMap := make(map[string]*apisv1alpha2.APIExport, len(exports))
	for _, e := range exports {
		exportMap[logicalcluster.From(e).String()+"/"+e.Name] = e
	}

	p := &workspacetype{
		Handler: admission.NewHandler(admission.Create, admission.Update),
		createAuthorizer: func(clusterName logicalcluster.Name, client kcpkubernetesclientset.ClusterInterface, opts delegated.Options) (authorizer.Authorizer, error) {
			return &fakeAuthorizer{decision: decision}, nil
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			if e, ok := exportMap[path.String()+"/"+name]; ok {
				return e, nil
			}
			return nil, apierrors.NewNotFound(apisv1alpha2.Resource("apiexports"), name)
		},
	}
	p.SetReadyFunc(func() bool { return true })
	return p
}

func newAPIExport(cluster logicalcluster.Name, name string) *apisv1alpha2.APIExport {
	return &apisv1alpha2.APIExport{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apisv1alpha2.SchemeGroupVersion.String(),
			Kind:       "APIExport",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
		},
	}
}

func TestValidate_DefaultAPIBindings(t *testing.T) {
	cowboys := newAPIExport(core.RootCluster, "cowboys")
	tenantCluster := logicalcluster.Name("root-test")

	tests := []struct {
		name         string
		op           admission.Operation
		newWT        *tenancyv1alpha1.WorkspaceType
		oldWT        *tenancyv1alpha1.WorkspaceType
		decision     authorizer.Decision
		exports      []*apisv1alpha2.APIExport
		wantForbid   bool
		wantContains string
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
			exports:  []*apisv1alpha2.APIExport{cowboys},
		},
		{
			name: "create with defaultAPIBindings denied when user lacks bind",
			op:   admission.Create,
			newWT: newWorkspaceType(tenantCluster, "bad",
				tenancyv1alpha1.APIExportReference{Path: "root", Export: "cowboys"},
			),
			decision:     authorizer.DecisionDeny,
			exports:      []*apisv1alpha2.APIExport{cowboys},
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
			exports:    []*apisv1alpha2.APIExport{cowboys},
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
			exports:  []*apisv1alpha2.APIExport{cowboys},
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
			name: "create denied when referenced APIExport cannot be resolved",
			op:   admission.Create,
			newWT: newWorkspaceType(tenantCluster, "z",
				tenancyv1alpha1.APIExportReference{Path: "some:other:cluster", Export: "missing"},
			),
			decision:   authorizer.DecisionAllow,
			wantForbid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPlugin(tt.decision, tt.exports...)
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
		})
	}
}
