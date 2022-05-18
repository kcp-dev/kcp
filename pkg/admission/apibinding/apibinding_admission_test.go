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
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/kubernetes"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
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

func TestValidate(t *testing.T) {
	tests := []struct {
		name           string
		attr           admission.Attributes
		authzDecision  authorizer.Decision
		authzError     error
		expectedErrors []string
	}{
		{
			name: "Create: passes with no reference",
			attr: createAttr(
				newAPIBinding().withName("test").APIBinding,
			),
		},
		{
			name: "Create: missing workspace reference workspaceName fails",
			attr: createAttr(
				newAPIBinding().withName("test").withWorkspaceReference("", "export").APIBinding,
			),
			expectedErrors: []string{"spec.reference.name: Required value"},
		},
		{
			name: "Create: missing workspace reference exportName fails",
			attr: createAttr(
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "").APIBinding,
			),
			expectedErrors: []string{"spec.reference.exportName: Required value"},
		},
		{
			name: "Create: complete workspace reference passes when authorized",
			attr: createAttr(
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "someExport").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Create: complete workspace reference fails with no authorization decision",
			attr: createAttr(
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "someExport").APIBinding,
			),
			authzDecision:  authorizer.DecisionNoOpinion,
			expectedErrors: []string{"missing verb='bind' permission on apiexports"},
		},
		{
			name: "Create: complete workspace reference fails when denied",
			attr: createAttr(
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "someExport").APIBinding,
			),
			authzDecision:  authorizer.DecisionDeny,
			expectedErrors: []string{"missing verb='bind' permission on apiexports"},
		},
		{
			name: "Create: complete workspace reference fails when there's an error checking authorization",
			attr: createAttr(
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "someExport").APIBinding,
			),
			authzError:     errors.New("some error here"),
			expectedErrors: []string{"unable to determine access to apiexports: some error here"},
		},
		//
		{
			name: "Update: missing workspace reference workspaceName fails",
			attr: updateAttr(
				newAPIBinding().withName("test").withWorkspaceReference("", "export").APIBinding,
				newAPIBinding().withName("test").withWorkspaceReference("workspace", "export").APIBinding,
			),
			expectedErrors: []string{"spec.reference.name: Required value"},
		},
		{
			name: "Update: missing workspace reference exportName fails",
			attr: updateAttr(
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "").APIBinding,
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "export").APIBinding,
			),
			expectedErrors: []string{"spec.reference.exportName: Required value"},
		},
		{
			name: "Update: complete workspace reference passes when authorized",
			attr: updateAttr(
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "someExport").APIBinding,
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "someExport").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Update: complete workspace reference fails with no authorization decision",
			attr: updateAttr(
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "someExport").APIBinding,
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "someExport").APIBinding,
			),
			authzDecision:  authorizer.DecisionNoOpinion,
			expectedErrors: []string{"missing verb='bind' permission on apiexports"},
		},
		{
			name: "Update: complete workspace reference fails when denied",
			attr: updateAttr(
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "someExport").APIBinding,
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "someExport").APIBinding,
			),
			authzDecision:  authorizer.DecisionDeny,
			expectedErrors: []string{"missing verb='bind' permission on apiexports"},
		},
		{
			name: "Update: complete workspace reference fails when there's an error checking authorization",
			attr: updateAttr(
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "someExport").APIBinding,
				newAPIBinding().withName("test").withWorkspaceReference("workspaceName", "someExport").APIBinding,
			),
			authzError:     errors.New("some error here"),
			expectedErrors: []string{"unable to determine access to apiexports: some error here"},
		},
		{
			name: "Update: transition from '' to binding passes",
			attr: updateAttr(
				newAPIBinding().
					withWorkspaceReference("workspaceName", "someExport").
					withPhase(apisv1alpha1.APIBindingPhaseBinding).APIBinding,
				newAPIBinding().
					withWorkspaceReference("workspaceName", "someExport").
					withPhase("").APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Update: transition from binding to bound passes",
			attr: updateAttr(
				newAPIBinding().
					withWorkspaceReference("workspaceName", "someExport").
					withPhase(apisv1alpha1.APIBindingPhaseBound).APIBinding,
				newAPIBinding().
					withWorkspaceReference("workspaceName", "someExport").
					withPhase(apisv1alpha1.APIBindingPhaseBinding).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
		{
			name: "Update: transition backwards from binding to '' fails",
			attr: updateAttr(
				newAPIBinding().
					withWorkspaceReference("workspaceName", "someExport").
					withPhase("").APIBinding,
				newAPIBinding().
					withWorkspaceReference("workspaceName", "someExport").
					withPhase(apisv1alpha1.APIBindingPhaseBinding).APIBinding,
			),
			authzDecision:  authorizer.DecisionAllow,
			expectedErrors: []string{`cannot transition from "Binding" to ""`},
		},
		{
			name: "Update: transition backwards from bound to '' fails",
			attr: updateAttr(
				newAPIBinding().
					withWorkspaceReference("workspaceName", "someExport").
					withPhase("").APIBinding,
				newAPIBinding().
					withWorkspaceReference("workspaceName", "someExport").
					withPhase(apisv1alpha1.APIBindingPhaseBound).APIBinding,
			),
			authzDecision:  authorizer.DecisionAllow,
			expectedErrors: []string{`cannot transition from "Bound" to ""`},
		},
		{
			name: "Update: transition backwards from bound to binding passes",
			attr: updateAttr(
				newAPIBinding().
					withWorkspaceReference("workspaceName", "someExport").
					withPhase(apisv1alpha1.APIBindingPhaseBinding).APIBinding,
				newAPIBinding().
					withWorkspaceReference("workspaceName", "someExport").
					withPhase(apisv1alpha1.APIBindingPhaseBound).APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &apiBindingAdmission{
				Handler: admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: func(clusterName logicalcluster.Name, client kubernetes.ClusterInterface) (authorizer.Authorizer, error) {
					return &fakeAuthorizer{
						tc.authzDecision,
						tc.authzError,
					}, nil
				},
			}

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root:org")})

			err := o.Validate(ctx, tc.attr, nil)

			wantErr := len(tc.expectedErrors) > 0
			require.Equal(t, wantErr, err != nil)

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
		APIBinding: &apisv1alpha1.APIBinding{},
	}
}

func (b *bindingBuilder) withName(name string) *bindingBuilder {
	b.Name = name
	return b
}

func (b *bindingBuilder) withWorkspaceReference(workspaceName, exportName string) *bindingBuilder {
	b.Spec.Reference.Workspace = &apisv1alpha1.WorkspaceExportReference{
		WorkspaceName: workspaceName,
		ExportName:    exportName,
	}
	return b
}

func (b *bindingBuilder) withPhase(phase apisv1alpha1.APIBindingPhaseType) *bindingBuilder {
	b.Status.Phase = phase
	return b
}

func (b *bindingBuilder) withInitializers(initializers ...string) *bindingBuilder {
	b.Status.Initializers = initializers
	return b
}

func TestAdmit(t *testing.T) {
	tests := []struct {
		name           string
		attr           admission.Attributes
		authzDecision  authorizer.Decision
		authzError     error
		validateObj    func(*apisv1alpha1.APIBinding) error
		expectedErrors []string
	}{
		{
			name: "Update: adds the initializer when moving from no phase",
			attr: updateAttr(
				newAPIBinding().
					withPhase(apisv1alpha1.APIBindingPhaseBinding).
					APIBinding,
				newAPIBinding().
					APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			validateObj: func(object *apisv1alpha1.APIBinding) error {
				if diff := cmp.Diff(object.Status.Initializers, []string{apisv1alpha1.DefaultAPIBindingInitializer}); diff != "" {
					return fmt.Errorf("invalid initializers: %v", diff)
				}
				return nil
			},
		},
		{
			name: "Update: adds the initializer when moving from some other phase",
			attr: updateAttr(
				newAPIBinding().
					withPhase(apisv1alpha1.APIBindingPhaseBinding).
					APIBinding,
				newAPIBinding().
					withPhase(apisv1alpha1.APIBindingPhaseBound).
					APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			validateObj: func(object *apisv1alpha1.APIBinding) error {
				if diff := cmp.Diff(object.Status.Initializers, []string{apisv1alpha1.DefaultAPIBindingInitializer}); diff != "" {
					return fmt.Errorf("invalid initializers: %v", diff)
				}
				return nil
			},
		},
		{
			name: "Update: removes extraneous initializers when they are present moving into binding phase",
			attr: updateAttr(
				newAPIBinding().
					withPhase(apisv1alpha1.APIBindingPhaseBinding).
					APIBinding,
				newAPIBinding().
					withInitializers("whoa", "there", "buddy").
					APIBinding,
			),
			authzDecision: authorizer.DecisionAllow,
			validateObj: func(object *apisv1alpha1.APIBinding) error {
				if diff := cmp.Diff(object.Status.Initializers, []string{apisv1alpha1.DefaultAPIBindingInitializer}); diff != "" {
					return fmt.Errorf("invalid initializers: %v", diff)
				}
				return nil
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &apiBindingAdmission{
				Handler: admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: func(clusterName logicalcluster.Name, client kubernetes.ClusterInterface) (authorizer.Authorizer, error) {
					return &fakeAuthorizer{
						tc.authzDecision,
						tc.authzError,
					}, nil
				},
			}

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root:org")})

			err := o.Admit(ctx, tc.attr, nil)

			wantErr := len(tc.expectedErrors) > 0
			require.Equal(t, wantErr, err != nil)

			if err != nil {
				t.Logf("Got admission errors: %v", err)
				for _, expected := range tc.expectedErrors {
					require.Contains(t, err.Error(), expected)
				}
			}

			if tc.validateObj != nil {
				u, ok := tc.attr.GetObject().(*unstructured.Unstructured)
				require.True(t, ok, "unexpected type %T", tc.attr.GetObject())

				apiBinding := &apisv1alpha1.APIBinding{}
				require.NoError(t,
					runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, apiBinding),
					"failed to convert unstructured to APIBinding",
				)

				require.NoError(t, tc.validateObj(apiBinding))
			}
		})
	}
}
