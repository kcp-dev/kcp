/*
Copyright 2023 The KCP Authors.

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

package apiexportendpointslice

import (
	"context"
	"errors"
	"strings"
	"testing"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
)

func createAttr(slice *apisv1alpha1.APIExportEndpointSlice) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(slice),
		nil,
		apisv1alpha1.Kind("APIExportEndpointSlice").WithVersion("v1alpha1"),
		"",
		slice.Name,
		apisv1alpha1.Resource("apiexportendpointslices").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func updateAttr(newSlice, oldSlice *apisv1alpha1.APIExportEndpointSlice) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(newSlice),
		helpers.ToUnstructuredOrDie(oldSlice),
		apisv1alpha1.Kind("APIExportEndpointSlice").WithVersion("v1alpha1"),
		"",
		newSlice.Name,
		apisv1alpha1.Resource("apiexportendpointslices").WithVersion("v1alpha1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name           string
		attr           admission.Attributes
		authzDecision  authorizer.Decision
		authzError     error
		expectedErrors []string
	}{
		{
			name: "Create: fails without reference",
			attr: createAttr(
				newAPIExportEndpointSlice().withName("test").APIExportEndpointSlice,
			),
			expectedErrors: []string{"spec.export.name: Required value"},
		},
		{
			name: "Create: missing workspace reference exportName fails",
			attr: createAttr(
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root-org-workspaceName"), "").APIExportEndpointSlice,
			),
			expectedErrors: []string{"spec.export.name: Required value"},
		},
		{
			name: "Create: complete workspaceName reference passes when authorized",
			attr: createAttr(
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIExportEndpointSlice,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Create: root reference passes when authorized",
			attr: createAttr(
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root"), "someExport").APIExportEndpointSlice,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Create: complete root absolute workspace reference passes when authorized",
			attr: createAttr(
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root"), "someExport").APIExportEndpointSlice,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Create: complete non-root absolute workspace reference passes when authorized",
			attr: createAttr(
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root:aunt"), "someExport").APIExportEndpointSlice,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Create: complete workspace reference fails with no authorization decision",
			attr: createAttr(
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIExportEndpointSlice,
			),
			authzDecision:  authorizer.DecisionNoOpinion,
			expectedErrors: []string{`no permission to bind to export root:org:workspaceName:someExport`},
		},
		{
			name: "Create: complete workspace reference fails when denied",
			attr: createAttr(
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIExportEndpointSlice,
			),
			authzDecision:  authorizer.DecisionDeny,
			expectedErrors: []string{`no permission to bind to export root:org:workspaceName:someExport`},
		},
		{
			name: "Create: complete workspace reference fails when there's an error checking authorization",
			attr: createAttr(
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIExportEndpointSlice,
			),
			authzError:     errors.New("some error here"),
			expectedErrors: []string{"no permission to bind to export root:org:workspaceName:someExport"},
		},
		{
			name: "Update: complete workspace reference passes when authorized",
			attr: updateAttr(
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIExportEndpointSlice,
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIExportEndpointSlice,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Update: complete workspace reference fails with no authorization decision",
			attr: updateAttr(
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIExportEndpointSlice,
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "anotherExport").APIExportEndpointSlice,
			),
			authzDecision:  authorizer.DecisionNoOpinion,
			expectedErrors: []string{`no permission to bind to export root:org:workspaceName:someExport`},
		},
		{
			name: "Update: complete workspace reference fails when denied",
			attr: updateAttr(
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIExportEndpointSlice,
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "anotherExport").APIExportEndpointSlice,
			),
			authzDecision:  authorizer.DecisionDeny,
			expectedErrors: []string{`no permission to bind to export root:org:workspaceName:someExport`},
		},
		{
			name: "Update: complete workspace reference fails when there's an error checking authorization",
			attr: updateAttr(
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIExportEndpointSlice,
				newAPIExportEndpointSlice().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "anotherExport").APIExportEndpointSlice,
			),
			authzError:     errors.New("some error here"),
			expectedErrors: []string{"no permission to bind to export root:org:workspaceName:someExport"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &apiExportEndpointSliceAdmission{
				Handler: admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: func(clusterName logicalcluster.Name, client kcpkubernetesclientset.ClusterInterface, opts delegated.Options) (authorizer.Authorizer, error) {
					return &fakeAuthorizer{
						tc.authzDecision,
						tc.authzError,
					}, nil
				},
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
					switch path.Join(name).String() {
					case "root:org:workspaceName:someExport", "root-org-workspaceName:someExport":
						return newExport(logicalcluster.NewPath("root:org:workspaceName"), name).APIExport, nil
					case "root:aunt:someExport", "root-aunt:someExport":
						return newExport(logicalcluster.NewPath("root:aunt"), name).APIExport, nil
					case "root:org:sibling:someExport", "root-org-sibling:someExport":
						return newExport(logicalcluster.NewPath("root:org:sibling"), name).APIExport, nil
					case "root:org:someExport", "root-org:someExport":
						return newExport(logicalcluster.NewPath("root:org"), name).APIExport, nil
					case "root:some-other-org:bla:someExport", "root-some-other-org-bla:someExport":
						return newExport(logicalcluster.NewPath("root:some-other-org:bla"), name).APIExport, nil
					case "root:someExport":
						return newExport(logicalcluster.NewPath("root"), name).APIExport, nil
					}
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiexports"), path.Join(name).String())
				},
			}

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.From(tc.attr.GetObject().(metav1.Object))})

			err := o.Validate(ctx, tc.attr, nil)

			wantErr := len(tc.expectedErrors) > 0
			require.Equal(t, wantErr, err != nil, "err: %v", err)

			if err != nil {
				t.Logf("Got admission errors: %v", err)
				for _, expected := range tc.expectedErrors {
					require.Contains(t, err.Error(), expected)
				}
			}
		})
	}
}

type fakeAuthorizer struct {
	authorized authorizer.Decision
	err        error
}

func (a *fakeAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	return a.authorized, "reason", a.err
}

type bindingBuilder struct {
	*apisv1alpha1.APIExportEndpointSlice
}

func newAPIExportEndpointSlice() *bindingBuilder {
	return &bindingBuilder{
		APIExportEndpointSlice: &apisv1alpha1.APIExportEndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: "root-org-ws",
				},
			},
		},
	}
}

func (b *bindingBuilder) withName(name string) *bindingBuilder {
	b.Name = name
	return b
}

func (b *bindingBuilder) withReference(path logicalcluster.Path, exportName string) *bindingBuilder {
	b.Spec.APIExport = apisv1alpha1.ExportBindingReference{
		Path: path.String(),
		Name: exportName,
	}
	return b
}

type apiExportBuilder struct {
	*apisv1alpha1.APIExport
}

func newExport(path logicalcluster.Path, name string) apiExportBuilder {
	clusterName := strings.ReplaceAll(path.String(), ":", "-")
	return apiExportBuilder{APIExport: &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey:         clusterName,
				core.LogicalClusterPathAnnotationKey: path.String(),
			},
		},
	}}
}
