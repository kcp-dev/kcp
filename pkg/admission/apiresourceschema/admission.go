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

package apiresourceschema

import (
	"context"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

const (
	PluginName = "tenancy.kcp.io/APIResourceSchema"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &apiResourceSchemaValidation{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

type apiResourceSchemaValidation struct {
	*admission.Handler
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&apiResourceSchemaValidation{})

// Validate does validation of a APIResourceSchema for create and update.
func (o *apiResourceSchemaValidation) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != apisv1alpha1.Resource("apiresourceschemas") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	schema := &apisv1alpha1.APIResourceSchema{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, schema); err != nil {
		return fmt.Errorf("failed to convert unstructured to APIResourceSchema: %w", err)
	}

	// first all steps where we need no lister
	var old *apisv1alpha1.APIResourceSchema
	switch a.GetOperation() {
	case admission.Create:
		if errs := ValidateAPIResourceSchema(ctx, schema); len(errs) > 0 {
			return admission.NewForbidden(a, fmt.Errorf("%v", errs))
		}

	case admission.Update:
		u, ok = a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetOldObject())
		}
		old = &apisv1alpha1.APIResourceSchema{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, old); err != nil {
			return fmt.Errorf("failed to convert unstructured to APIResourceSchema: %w", err)
		}

		if errs := ValidateAPIResourceSchemaUpdate(ctx, schema, old); len(errs) > 0 {
			return admission.NewForbidden(a, fmt.Errorf("%v", errs))
		}
	}

	return nil
}
