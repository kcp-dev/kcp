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
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/stretchr/testify/require"
	"testing"
)

type bindingBuilder struct {
	*apisv1alpha1.APIBinding
}

func newAPIBinding() *bindingBuilder {
	return &bindingBuilder{
		APIBinding: &apisv1alpha1.APIBinding{},
	}
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

func TestValidateAPIBinding(t *testing.T) {
	tests := []struct {
		name    string
		binding *bindingBuilder
		wantErr bool
	}{
		{
			name:    "Empty passes",
			binding: newAPIBinding(),
			wantErr: false,
		},
		{
			name:    "Missing workspace reference workspaceName fails",
			binding: newAPIBinding().withWorkspaceReference("", "export"),
			wantErr: true,
		},
		{
			name:    "Missing workspace reference exportName fails",
			binding: newAPIBinding().withWorkspaceReference("workspaceName", ""),
			wantErr: true,
		},
		{
			name:    "Complete workspace reference passes",
			binding: newAPIBinding().withWorkspaceReference("workspaceName", "someExport"),
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateAPIBinding(tc.binding.APIBinding)
			require.Equal(t, tc.wantErr, len(errs) > 0)
		})
	}
}

func TestValidateAPIBindingUpdate(t *testing.T) {
	tests := []struct {
		name       string
		oldBinding *bindingBuilder
		newBinding *bindingBuilder
		wantErr    bool
	}{
		// Standard validations
		{
			name:       "Missing workspace reference workspaceName fails",
			oldBinding: newAPIBinding().withWorkspaceReference("someWorkspace", "export"),
			newBinding: newAPIBinding().withWorkspaceReference("", "export"),
			wantErr:    true,
		},
		{
			name:       "Missing workspace reference exportName fails",
			oldBinding: newAPIBinding().withWorkspaceReference("workspaceName", "someExport"),
			newBinding: newAPIBinding().withWorkspaceReference("workspaceName", ""),
			wantErr:    true,
		},
		{
			name:       "Change workspace reference passes",
			oldBinding: newAPIBinding().withWorkspaceReference("workspaceName", "someExport"),
			newBinding: newAPIBinding().withWorkspaceReference("workspaceName", "someOtherExport"),
			wantErr:    false,
		},

		// Update validations
		{
			name: "Transition from '' to binding passes",
			oldBinding: newAPIBinding().
				withWorkspaceReference("workspaceName", "someExport").
				withPhase(""),
			newBinding: newAPIBinding().
				withWorkspaceReference("workspaceName", "someExport").
				withPhase(apisv1alpha1.APIBindingPhaseBinding),
			wantErr: false,
		},
		{
			name: "Transition from binding to bound passes",
			oldBinding: newAPIBinding().
				withWorkspaceReference("workspaceName", "someExport").
				withPhase(apisv1alpha1.APIBindingPhaseBinding),
			newBinding: newAPIBinding().
				withWorkspaceReference("workspaceName", "someExport").
				withPhase(apisv1alpha1.APIBindingPhaseBound),
			wantErr: false,
		},
		{
			name: "Transition backwards binding to '' fails",
			oldBinding: newAPIBinding().
				withWorkspaceReference("workspaceName", "someExport").
				withPhase(apisv1alpha1.APIBindingPhaseBinding),
			newBinding: newAPIBinding().
				withWorkspaceReference("workspaceName", "someExport").
				withPhase(""),
			wantErr: true,
		},
		{
			name: "Transition backwards bound to binding fails",
			oldBinding: newAPIBinding().
				withWorkspaceReference("workspaceName", "someExport").
				withPhase(apisv1alpha1.APIBindingPhaseBound),
			newBinding: newAPIBinding().
				withWorkspaceReference("workspaceName", "someExport").
				withPhase(apisv1alpha1.APIBindingPhaseBinding),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateAPIBindingUpdate(tc.oldBinding.APIBinding, tc.newBinding.APIBinding)
			require.Equal(t, tc.wantErr, len(errs) > 0)
		})
	}
}
