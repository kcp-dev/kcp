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

package apibinding

import (
	"context"
	"crypto/sha256"
	"errors"
	"math/big"
	"strings"
	"testing"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
)

func createAttr(apiBinding *apisv1alpha1.APIBinding) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(apiBinding),
		nil,
		apisv1alpha1.Kind("APIBinding").WithVersion("v1alpha1"),
		"",
		apiBinding.Name,
		apisv1alpha1.Resource("apibindings").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func updateAttr(newAPIBinding, oldAPIBinding *apisv1alpha1.APIBinding) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(newAPIBinding),
		helpers.ToUnstructuredOrDie(oldAPIBinding),
		apisv1alpha1.Kind("APIBinding").WithVersion("v1alpha1"),
		"",
		newAPIBinding.Name,
		apisv1alpha1.Resource("apibindings").WithVersion("v1alpha1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func TestAdmit(t *testing.T) {
	tests := []struct {
		name           string
		attr           admission.Attributes
		authzDecision  authorizer.Decision
		authzError     error
		expectedErrors []string
		expectedObject runtime.Object
	}{
		{
			name: "Create: passes with no reference",
			attr: createAttr(
				newAPIBinding().withName("test").APIBinding,
			),
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBinding().withName("test").APIBinding),
		},
		{
			name: "Create: with absolute workspace reference",
			attr: createAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:aunt"), "someExport").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:aunt"), "someExport").
				withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-aunt:someExport")).APIBinding),
		},
		{
			name: "Create: with relative export reference",
			attr: createAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.Path{}, "someExport").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBinding().withName("test").withReference(logicalcluster.Path{}, "someExport").
				withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-ws:someExport")).APIBinding),
		},
		{
			name: "Create: with root export reference",
			attr: createAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root"), "someExport").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root"), "someExport").
				withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root:someExport")).APIBinding),
		},
		{
			name: "Update: with export reference",
			attr: updateAttr(
				newAPIBinding().withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIBinding,
				newAPIBinding().withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIBinding,
			),
			authzDecision:  authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBinding().withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIBinding),
		},
		{
			name: "Update: with changing export reference",
			attr: updateAttr(
				newAPIBinding().withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIBinding,
				newAPIBinding().withReference(logicalcluster.NewPath("root:aunt"), "someExport").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBinding().withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
				withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding),
		},
		{
			name: "Update: with changing export reference, absolute to relative",
			attr: updateAttr(
				newAPIBinding().withReference(logicalcluster.Path{}, "someExport").APIBinding,
				newAPIBinding().withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBinding().withReference(logicalcluster.Path{}, "someExport").
				withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-ws:someExport")).APIBinding),
		},
		{
			name: "Update: no lookup if not changing",
			attr: updateAttr(
				newAPIBinding().withReference(logicalcluster.NewPath("root:non-existing"), "someExport").APIBinding,
				newAPIBinding().withReference(logicalcluster.NewPath("root:non-existing"), "someExport").APIBinding,
			),
			authzDecision:  authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBinding().withReference(logicalcluster.NewPath("root:non-existing"), "someExport").APIBinding),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &apiBindingAdmission{
				Handler: admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: func(clusterName logicalcluster.Name, client kcpkubernetesclientset.ClusterInterface) (authorizer.Authorizer, error) {
					return &fakeAuthorizer{
						tc.authzDecision,
						tc.authzError,
					}, nil
				},
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
					switch path.Join(name).String() {
					case "root:org:ws:someExport", "root-org-ws:someExport":
						return newExport(logicalcluster.NewPath("root:org:ws"), "someExport").APIExport, nil
					case "root:aunt:someExport", "root-aunt:someExport":
						return newExport(logicalcluster.NewPath("root:aunt"), "someExport").APIExport, nil
					case "root:org:workspaceName:someExport", "root-org-workspaceName:someExport":
						return newExport(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIExport, nil
					case "root:org:foo:someExport", "root-org-foo:someExport":
						return newExport(logicalcluster.NewPath("root:org:foo"), "someExport").APIExport, nil

						// intentionally not the root export. Binding without looking up the root exports is needed for bootstrapping.
					}
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiexports"), name)
				},
			}

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.From(tc.attr.GetObject().(metav1.Object))})

			err := o.Admit(ctx, tc.attr, nil)

			wantErr := len(tc.expectedErrors) > 0
			require.Equal(t, wantErr, err != nil, "unexpected error: %v", err)

			if err != nil {
				t.Logf("Got admission errors: %v", err)
				for _, expected := range tc.expectedErrors {
					require.Contains(t, err.Error(), expected)
				}
			} else {
				require.Equal(t, tc.expectedObject, tc.attr.GetObject())
			}
		})
	}
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
				newAPIBinding().withName("test").APIBinding,
			),
			expectedErrors: []string{"spec.reference.export: Required value"},
		},
		{
			name: "Create: missing workspace reference exportName fails",
			attr: createAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root-org-workspaceName"), "").APIBinding,
			),
			expectedErrors: []string{"spec.reference.export.name: Required value"},
		},
		{
			name: "Create: complete workspaceName reference passes when authorized",
			attr: createAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Create: root reference passes when authorized",
			attr: createAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root:someExport")).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Create: complete root absolute workspace reference passes when authorized",
			attr: createAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root:someExport")).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Create: complete non-root absolute workspace reference passes when authorized",
			attr: createAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:aunt"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-aunt:someExport")).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Create: complete workspace reference fails with no authorization decision",
			attr: createAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
			),
			authzDecision:  authorizer.DecisionNoOpinion,
			expectedErrors: []string{`no permission to bind to export root:org:workspaceName:someExport`},
		},
		{
			name: "Create: complete workspace reference fails when denied",
			attr: createAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
			),
			authzDecision:  authorizer.DecisionDeny,
			expectedErrors: []string{`no permission to bind to export root:org:workspaceName:someExport`},
		},
		{
			name: "Create: complete workspace reference fails when there's an error checking authorization",
			attr: createAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
			),
			authzError:     errors.New("some error here"),
			expectedErrors: []string{"no permission to bind to export root:org:workspaceName:someExport"},
		},
		{
			name: "Update: missing workspace reference exportName fails",
			attr: updateAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "").APIBinding,
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "export").APIBinding,
			),
			expectedErrors: []string{"spec.reference.export.name: Required value"},
		},
		{
			name: "Update: complete workspace reference passes when authorized",
			attr: updateAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Update: fails when export label is missing",
			attr: updateAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIBinding,
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
			),
			authzDecision:  authorizer.DecisionAllow,
			expectedErrors: []string{"metadata.labels[internal.apis.kcp.dev/export]: Invalid value: \"\": must be set to \"avKSFa3bDPry0NIAl3ECroTdaXPBY1dHReOilE\""},
		},
		{
			name: "Update: fails when export label is wrong",
			attr: updateAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someOtherExport")).APIBinding,
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
			),
			authzDecision:  authorizer.DecisionAllow,
			expectedErrors: []string{"metadata.labels[internal.apis.kcp.dev/export]: Invalid value: \"3O8yqCs4In4wWBmfjGQsNlMZi9SOHb8n1xCt1o\": must be set to \"avKSFa3bDPry0NIAl3ECroTdaXPBY1dHReOilE\""},
		},
		{
			name: "Update: complete workspace reference fails with no authorization decision",
			attr: updateAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "anotherExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:anotherExport")).APIBinding,
			),
			authzDecision:  authorizer.DecisionNoOpinion,
			expectedErrors: []string{`no permission to bind to export root:org:workspaceName:someExport`},
		},
		{
			name: "Update: complete workspace reference fails when denied",
			attr: updateAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "anotherExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:anotherExport")).APIBinding,
			),
			authzDecision:  authorizer.DecisionDeny,
			expectedErrors: []string{`no permission to bind to export root:org:workspaceName:someExport`},
		},
		{
			name: "Update: complete workspace reference fails when there's an error checking authorization",
			attr: updateAttr(
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
				newAPIBinding().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "anotherExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:anotherExport")).APIBinding,
			),
			authzError:     errors.New("some error here"),
			expectedErrors: []string{"no permission to bind to export root:org:workspaceName:someExport"},
		},
		{
			name: "Update: transition from '' to binding passes",
			attr: updateAttr(
				newAPIBinding().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha1.APIBindingPhaseBinding).APIBinding,
				newAPIBinding().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase("").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Update: transition from binding to bound passes",
			attr: updateAttr(
				newAPIBinding().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha1.APIBindingPhaseBound).APIBinding,
				newAPIBinding().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha1.APIBindingPhaseBinding).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Update: transition backwards from binding to '' fails",
			attr: updateAttr(
				newAPIBinding().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase("").APIBinding,
				newAPIBinding().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha1.APIBindingPhaseBinding).APIBinding,
			),
			authzDecision:  authorizer.DecisionAllow,
			expectedErrors: []string{`cannot transition from "Binding" to ""`},
		},
		{
			name: "Update: transition backwards from bound to '' fails",
			attr: updateAttr(
				newAPIBinding().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase("").APIBinding,
				newAPIBinding().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha1.APIBindingPhaseBound).APIBinding,
			),
			authzDecision:  authorizer.DecisionAllow,
			expectedErrors: []string{`cannot transition from "Bound" to ""`},
		},
		{
			name: "Update: transition backwards from bound to binding passes",
			attr: updateAttr(
				newAPIBinding().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha1.APIBindingPhaseBinding).APIBinding,
				newAPIBinding().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha1.APIBindingPhaseBound).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &apiBindingAdmission{
				Handler: admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: func(clusterName logicalcluster.Name, client kcpkubernetesclientset.ClusterInterface) (authorizer.Authorizer, error) {
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

						// intentionally not the root export. Binding without looking up the root exports is needed for bootstrapping.
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
	*apisv1alpha1.APIBinding
}

func newAPIBinding() *bindingBuilder {
	return &bindingBuilder{
		APIBinding: &apisv1alpha1.APIBinding{
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
	b.Spec.Reference.Export = &apisv1alpha1.ExportBindingReference{
		Path: path.String(),
		Name: exportName,
	}
	return b
}

func (b *bindingBuilder) withLabel(k, v string) *bindingBuilder {
	if b.Labels == nil {
		b.Labels = make(map[string]string)
	}
	b.Labels[k] = v
	return b
}

func (b *bindingBuilder) withPhase(phase apisv1alpha1.APIBindingPhaseType) *bindingBuilder {
	b.Status.Phase = phase
	return b
}

func toSha224Base62(s string) string {
	return toBase62(sha256.Sum224([]byte(s)))
}

func toBase62(hash [28]byte) string {
	var i big.Int
	i.SetBytes(hash[:])
	return i.Text(62)
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
				logicalcluster.AnnotationKey:            clusterName,
				tenancy.LogicalClusterPathAnnotationKey: path.String(),
			},
		},
	}}
}
