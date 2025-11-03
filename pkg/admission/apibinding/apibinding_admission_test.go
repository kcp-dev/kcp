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
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
)

func createAttrV1Alpha2(apiBinding *apisv1alpha2.APIBinding) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(apiBinding),
		nil,
		apisv1alpha2.Kind("APIBinding").WithVersion("v1alpha2"),
		"",
		apiBinding.Name,
		apisv1alpha2.Resource("apibindings").WithVersion("v1alpha2"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func updateAttrV1Alpha2(newAPIBinding, oldAPIBinding *apisv1alpha2.APIBinding) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(newAPIBinding),
		helpers.ToUnstructuredOrDie(oldAPIBinding),
		apisv1alpha2.Kind("APIBinding").WithVersion("v1alpha2"),
		"",
		newAPIBinding.Name,
		apisv1alpha2.Resource("apibindings").WithVersion("v1alpha2"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func createAttrV1Alpha1(apiBinding *apisv1alpha1.APIBinding) admission.Attributes {
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

func updateAttrV1Alpha1(newAPIBinding, oldAPIBinding *apisv1alpha1.APIBinding) admission.Attributes {
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
			attr: createAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").APIBinding,
			),
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBindingV1Alpha2().withName("test").APIBinding),
		},
		{
			name: "Create v1alpha1: passes with no reference",
			attr: createAttrV1Alpha1(
				newAPIBindingV1Alpha1().withName("test").APIBinding,
			),
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBindingV1Alpha1().withName("test").APIBinding),
		},
		{
			name: "Create: with absolute workspace reference",
			attr: createAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:aunt"), "someExport").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:aunt"), "someExport").
				withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-aunt:someExport")).APIBinding),
		},
		{
			name: "Create v1alpha1: with absolute workspace reference",
			attr: createAttrV1Alpha1(
				newAPIBindingV1Alpha1().withName("test").withReference(logicalcluster.NewPath("root:aunt"), "someExport").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBindingV1Alpha1().withName("test").withReference(logicalcluster.NewPath("root:aunt"), "someExport").
				withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-aunt:someExport")).APIBinding),
		},
		{
			name: "Create: with relative export reference",
			attr: createAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.Path{}, "someExport").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.Path{}, "someExport").
				withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-ws:someExport")).APIBinding),
		},
		{
			name: "Create: with root export reference",
			attr: createAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root"), "someExport").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root"), "someExport").
				withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root:someExport")).APIBinding),
		},
		{
			name: "Update: with export reference",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIBinding,
				newAPIBindingV1Alpha2().withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIBinding,
			),
			authzDecision:  authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBindingV1Alpha2().withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIBinding),
		},
		{
			name: "Update: with changing export reference",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIBinding,
				newAPIBindingV1Alpha2().withReference(logicalcluster.NewPath("root:aunt"), "someExport").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBindingV1Alpha2().withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
				withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding),
		},
		{
			name: "Update: with changing export reference, absolute to relative",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().withReference(logicalcluster.Path{}, "someExport").APIBinding,
				newAPIBindingV1Alpha2().withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBindingV1Alpha2().withReference(logicalcluster.Path{}, "someExport").
				withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-ws:someExport")).APIBinding),
		},
		{
			name: "Update v1alpha1: with changing export reference, absolute to relative",
			attr: updateAttrV1Alpha1(
				newAPIBindingV1Alpha1().withReference(logicalcluster.Path{}, "someExport").APIBinding,
				newAPIBindingV1Alpha1().withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBindingV1Alpha1().withReference(logicalcluster.Path{}, "someExport").
				withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-ws:someExport")).APIBinding),
		},
		{
			name: "Update: no lookup if not changing",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().withReference(logicalcluster.NewPath("root:non-existing"), "someExport").APIBinding,
				newAPIBindingV1Alpha2().withReference(logicalcluster.NewPath("root:non-existing"), "someExport").APIBinding,
			),
			authzDecision:  authorizer.DecisionAllow,
			expectedObject: helpers.ToUnstructuredOrDie(newAPIBindingV1Alpha2().withReference(logicalcluster.NewPath("root:non-existing"), "someExport").APIBinding),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &apiBindingAdmission{
				Handler: admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: func(clusterName logicalcluster.Name, client kcpkubernetesclientset.ClusterInterface, opts delegated.Options) (authorizer.Authorizer, error) {
					return &fakeAuthorizer{
						tc.authzDecision,
						tc.authzError,
					}, nil
				},
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
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
					return nil, apierrors.NewNotFound(apisv1alpha2.Resource("apiexports"), name)
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
			attr: createAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").APIBinding,
			),
			expectedErrors: []string{"spec.reference.export: Required value"},
		},
		{
			name: "Create v1alpha1: fails without reference",
			attr: createAttrV1Alpha1(
				newAPIBindingV1Alpha1().withName("test").APIBinding,
			),
			expectedErrors: []string{"spec.reference.export: Required value"},
		},
		{
			name: "Create: missing workspace reference exportName fails",
			attr: createAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root-org-workspaceName"), "").APIBinding,
			),
			expectedErrors: []string{"spec.reference.export.name: Required value"},
		},
		{
			name: "Create v1alpha1: missing workspace reference exportName fails",
			attr: createAttrV1Alpha1(
				newAPIBindingV1Alpha1().withName("test").withReference(logicalcluster.NewPath("root-org-workspaceName"), "").APIBinding,
			),
			expectedErrors: []string{"spec.reference.export.name: Required value"},
		},
		{
			name: "Create: complete workspaceName reference passes when authorized",
			attr: createAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Create: root reference passes when authorized",
			attr: createAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root:someExport")).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Create v1alpha1: root reference passes when authorized",
			attr: createAttrV1Alpha1(
				newAPIBindingV1Alpha1().withName("test").withReference(logicalcluster.NewPath("root"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root:someExport")).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Create: complete root absolute workspace reference passes when authorized",
			attr: createAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root:someExport")).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Create: complete non-root absolute workspace reference passes when authorized",
			attr: createAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:aunt"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-aunt:someExport")).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Create: complete workspace reference fails with no authorization decision",
			attr: createAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
			),
			authzDecision:  authorizer.DecisionNoOpinion,
			expectedErrors: []string{`no permission to bind to export root:org:workspaceName:someExport`},
		},
		{
			name: "Create: complete workspace reference fails when denied",
			attr: createAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
			),
			authzDecision:  authorizer.DecisionDeny,
			expectedErrors: []string{`no permission to bind to export root:org:workspaceName:someExport`},
		},
		{
			name: "Create: complete workspace reference fails when there's an error checking authorization",
			attr: createAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
			),
			authzError:     errors.New("some error here"),
			expectedErrors: []string{"no permission to bind to export root:org:workspaceName:someExport"},
		},
		{
			name: "Update: missing workspace reference exportName fails",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "").APIBinding,
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "export").APIBinding,
			),
			expectedErrors: []string{"spec.reference.export.name: Required value"},
		},
		{
			name: "Update: complete workspace reference passes when authorized",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Update: fails when export label is missing",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").APIBinding,
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
			),
			authzDecision:  authorizer.DecisionAllow,
			expectedErrors: []string{"metadata.labels[internal.apis.kcp.io/export]: Invalid value: \"\": must be set to \"avKSFa3bDPry0NIAl3ECroTdaXPBY1dHReOilE\""},
		},
		{
			name: "Update: fails when export label is wrong",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someOtherExport")).APIBinding,
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
			),
			authzDecision:  authorizer.DecisionAllow,
			expectedErrors: []string{"metadata.labels[internal.apis.kcp.io/export]: Invalid value: \"3O8yqCs4In4wWBmfjGQsNlMZi9SOHb8n1xCt1o\": must be set to \"avKSFa3bDPry0NIAl3ECroTdaXPBY1dHReOilE\""},
		},
		{
			name: "Update: complete workspace reference fails with no authorization decision",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "anotherExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:anotherExport")).APIBinding,
			),
			authzDecision:  authorizer.DecisionNoOpinion,
			expectedErrors: []string{`no permission to bind to export root:org:workspaceName:someExport`},
		},
		{
			name: "Update: complete workspace reference fails when denied",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "anotherExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:anotherExport")).APIBinding,
			),
			authzDecision:  authorizer.DecisionDeny,
			expectedErrors: []string{`no permission to bind to export root:org:workspaceName:someExport`},
		},
		{
			name: "Update: complete workspace reference fails when there's an error checking authorization",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).APIBinding,
				newAPIBindingV1Alpha2().withName("test").withReference(logicalcluster.NewPath("root:org:workspaceName"), "anotherExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:anotherExport")).APIBinding,
			),
			authzError:     errors.New("some error here"),
			expectedErrors: []string{"no permission to bind to export root:org:workspaceName:someExport"},
		},
		{
			name: "Update: transition from '' to binding passes",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha2.APIBindingPhaseBinding).APIBinding,
				newAPIBindingV1Alpha2().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase("").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Update v1alpha1: transition from '' to binding passes",
			attr: updateAttrV1Alpha1(
				newAPIBindingV1Alpha1().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha1.APIBindingPhaseBinding).APIBinding,
				newAPIBindingV1Alpha1().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase("").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Update: transition from binding to bound passes",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha2.APIBindingPhaseBound).APIBinding,
				newAPIBindingV1Alpha2().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha2.APIBindingPhaseBinding).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Update: transition backwards from binding to '' fails",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase("").APIBinding,
				newAPIBindingV1Alpha2().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha2.APIBindingPhaseBinding).APIBinding,
			),
			authzDecision:  authorizer.DecisionAllow,
			expectedErrors: []string{`cannot transition from "Binding" to ""`},
		},
		{
			name: "Update: transition backwards from bound to '' fails",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase("").APIBinding,
				newAPIBindingV1Alpha2().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha2.APIBindingPhaseBound).APIBinding,
			),
			authzDecision:  authorizer.DecisionAllow,
			expectedErrors: []string{`cannot transition from "Bound" to ""`},
		},
		{
			name: "Update: transition backwards from bound to binding passes",
			attr: updateAttrV1Alpha2(
				newAPIBindingV1Alpha2().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha2.APIBindingPhaseBinding).APIBinding,
				newAPIBindingV1Alpha2().
					withReference(logicalcluster.NewPath("root:org:workspaceName"), "someExport").
					withLabel(apisv1alpha1.InternalAPIBindingExportLabelKey, toSha224Base62("root-org-workspaceName:someExport")).
					withPhase(apisv1alpha2.APIBindingPhaseBound).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &apiBindingAdmission{
				Handler: admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: func(clusterName logicalcluster.Name, client kcpkubernetesclientset.ClusterInterface, opts delegated.Options) (authorizer.Authorizer, error) {
					return &fakeAuthorizer{
						tc.authzDecision,
						tc.authzError,
					}, nil
				},
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
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
					return nil, apierrors.NewNotFound(apisv1alpha2.Resource("apiexports"), path.Join(name).String())
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

type bindingBuilderV1Alpha2 struct {
	*apisv1alpha2.APIBinding
}

func newAPIBindingV1Alpha2() *bindingBuilderV1Alpha2 {
	return &bindingBuilderV1Alpha2{
		APIBinding: &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: "root-org-ws",
				},
			},
		},
	}
}

func (b *bindingBuilderV1Alpha2) withName(name string) *bindingBuilderV1Alpha2 {
	b.Name = name
	return b
}

func (b *bindingBuilderV1Alpha2) withReference(path logicalcluster.Path, exportName string) *bindingBuilderV1Alpha2 {
	b.Spec.Reference.Export = &apisv1alpha2.ExportBindingReference{
		Path: path.String(),
		Name: exportName,
	}
	return b
}

func (b *bindingBuilderV1Alpha2) withLabel(k, v string) *bindingBuilderV1Alpha2 {
	if b.Labels == nil {
		b.Labels = make(map[string]string)
	}
	b.Labels[k] = v
	return b
}

func (b *bindingBuilderV1Alpha2) withPhase(phase apisv1alpha2.APIBindingPhaseType) *bindingBuilderV1Alpha2 {
	b.Status.Phase = phase
	return b
}

type bindingBuilderV1Alpha1 struct {
	*apisv1alpha1.APIBinding
}

func newAPIBindingV1Alpha1() *bindingBuilderV1Alpha1 {
	return &bindingBuilderV1Alpha1{
		APIBinding: &apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: "root-org-ws",
				},
			},
		},
	}
}

func (b *bindingBuilderV1Alpha1) withName(name string) *bindingBuilderV1Alpha1 {
	b.Name = name
	return b
}

func (b *bindingBuilderV1Alpha1) withReference(path logicalcluster.Path, exportName string) *bindingBuilderV1Alpha1 {
	b.Spec.Reference.Export = &apisv1alpha1.ExportBindingReference{
		Path: path.String(),
		Name: exportName,
	}
	return b
}

func (b *bindingBuilderV1Alpha1) withLabel(k, v string) *bindingBuilderV1Alpha1 {
	if b.Labels == nil {
		b.Labels = make(map[string]string)
	}
	b.Labels[k] = v
	return b
}

func (b *bindingBuilderV1Alpha1) withPhase(phase apisv1alpha1.APIBindingPhaseType) *bindingBuilderV1Alpha1 {
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
	*apisv1alpha2.APIExport
}

func newExport(path logicalcluster.Path, name string) apiExportBuilder {
	clusterName := strings.ReplaceAll(path.String(), ":", "-")
	return apiExportBuilder{APIExport: &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey:         clusterName,
				core.LogicalClusterPathAnnotationKey: path.String(),
			},
		},
	}}
}

func TestValidateOverhangingPermissionClaims(t *testing.T) {
	tests := map[string]struct {
		annotations      func() map[string]string
		permissionClaims []apisv1alpha1.AcceptablePermissionClaim
		expectedError    string
	}{
		"NoAnnotations": {
			annotations:      func() map[string]string { return nil },
			permissionClaims: nil,
			expectedError:    "",
		},
		"EmptyJSON": {
			annotations: func() map[string]string {
				pc := apisv1alpha2.PermissionClaim{}
				data, err := json.Marshal(pc)
				if err != nil {
					t.Fatalf("failed to marshal: %v", err)
				}
				return map[string]string{
					apisv1alpha2.PermissionClaimsAnnotation: string(data),
				}
			},
			permissionClaims: nil,
			expectedError:    "failed to decode overhanging permission claims",
		},
		"EmptyPermissionClaimsAndAnnotation": {
			annotations: func() map[string]string {
				return map[string]string{
					apisv1alpha2.PermissionClaimsAnnotation: "[]",
				}
			},
			permissionClaims: []apisv1alpha1.AcceptablePermissionClaim{},
			expectedError:    "",
		},
		"ValidJSON": {
			annotations: func() map[string]string {
				s := []apisv1alpha2.PermissionClaim{{
					GroupResource: apisv1alpha2.GroupResource{
						Group:    "foo",
						Resource: "bar",
					},
					IdentityHash: "baz",
					Verbs:        []string{"get", "list"},
				}}
				data, err := json.Marshal(s)
				if err != nil {
					t.Fatalf("failed to marshal: %v", err)
				}
				return map[string]string{
					apisv1alpha2.ResourceSchemasAnnotation: string(data),
				}
			},
			permissionClaims: []apisv1alpha1.AcceptablePermissionClaim{
				{
					PermissionClaim: apisv1alpha1.PermissionClaim{
						GroupResource: apisv1alpha1.GroupResource{
							Group:    "foo",
							Resource: "bar",
						},
						All:          true,
						IdentityHash: "baz",
					},
				},
			},
			expectedError: "",
		},
		"MismatchInAnnotations": {
			annotations: func() map[string]string {
				s := []apisv1alpha2.PermissionClaim{
					{
						GroupResource: apisv1alpha2.GroupResource{
							Group:    "foo",
							Resource: "bar",
						},
						IdentityHash: "baz",
						Verbs:        []string{"get", "list"},
					},
					{
						GroupResource: apisv1alpha2.GroupResource{
							Group:    "foo",
							Resource: "baz",
						},
						IdentityHash: "bar",
						Verbs:        []string{"get"},
					},
				}
				data, err := json.Marshal(s)
				if err != nil {
					t.Fatalf("failed to marshal: %v", err)
				}
				return map[string]string{
					apisv1alpha2.PermissionClaimsAnnotation: string(data),
				}
			},
			permissionClaims: []apisv1alpha1.AcceptablePermissionClaim{
				{
					PermissionClaim: apisv1alpha1.PermissionClaim{
						GroupResource: apisv1alpha1.GroupResource{
							Group:    "foo",
							Resource: "bar",
						},
						All:          true,
						IdentityHash: "baz",
					},
				},
				{
					PermissionClaim: apisv1alpha1.PermissionClaim{
						GroupResource: apisv1alpha1.GroupResource{
							Group:    "test",
							Resource: "schema",
						},
						All:          true,
						IdentityHash: "random",
					},
				},
			},
			expectedError: "permission claims defined in annotation do not match permission claims defined in spec",
		},
		"InvalidJSON": {
			annotations: func() map[string]string {
				return map[string]string{
					apisv1alpha2.PermissionClaimsAnnotation: "invalid json",
				}
			},
			permissionClaims: nil,
			expectedError:    "failed to decode overhanging permission claims",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ae := &apisv1alpha1.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: tc.annotations(),
				},
				Spec: apisv1alpha1.APIBindingSpec{
					PermissionClaims: tc.permissionClaims,
				},
			}
			err := validateOverhangingPermissionClaims(context.TODO(), nil, ae)
			if tc.expectedError == "" {
				require.NoError(t, err)
			} else {
				require.Contains(t, err.Error(), tc.expectedError)
			}
		})
	}
}
