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

package apiexport

import (
	"context"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"

	builtinapiexport "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
	"github.com/kcp-dev/kcp/sdk/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

// PluginName is the name used to identify this admission webhook.
const PluginName = "apis.kcp.io/APIExport"

// Register registers the reserved name admission webhook.
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return NewAPIExportAdmission(builtinapiexport.IsBuiltInAPI), nil
		})
}

// APIExportAdmission is an admission plugin for checking APIExport validity.
type APIExportAdmission struct {
	*admission.Handler

	isBuiltIn func(apisv1alpha1.GroupResource) bool
}

// NewAPIExportAdmission constructs a new APIExportAdmission admission plugin.
func NewAPIExportAdmission(isBuiltIn func(apisv1alpha1.GroupResource) bool) *APIExportAdmission {
	return &APIExportAdmission{
		Handler:   admission.NewHandler(admission.Create, admission.Update),
		isBuiltIn: isBuiltIn,
	}
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&APIExportAdmission{})

// Validate ensures that the APIExport is valid.
func (e *APIExportAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != apisv1alpha1.Resource("apiexports") {
		return nil
	}

	if a.GetKind().GroupKind() != apisv1alpha1.Kind("APIExport") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	ae := &apisv1alpha1.APIExport{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, ae); err != nil {
		return fmt.Errorf("failed to convert unstructured to APIExport: %w", err)
	}

	for i, pc := range ae.Spec.PermissionClaims {
		if pc.IdentityHash == "" && !e.isBuiltIn(pc.GroupResource) && pc.Group != apis.GroupName {
			return admission.NewForbidden(a,
				field.Invalid(
					field.NewPath("spec").
						Child("permissionClaims").
						Index(i).
						Child("identityHash"),
					"",
					"identityHash is required for API types that are not built-in"))
		}
	}

	return nil
}
