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

package apiconversion

import (
	"context"
	"fmt"
	"io"

	"github.com/kcp-dev/logicalcluster/v3"

	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/conversion"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

const (
	PluginName = "apis.kcp.io/APIConversion"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &apiConversionAdmission{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

type apiConversionAdmission struct {
	*admission.Handler

	getAPIResourceSchema func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
}

// Ensure that the required admission interfaces are implemented.
var (
	_ admission.ValidationInterface     = (*apiConversionAdmission)(nil)
	_ admission.InitializationValidator = (*apiConversionAdmission)(nil)
	_ initializers.WantsKcpInformers    = (*apiConversionAdmission)(nil)
)

// Validate ensures all the conversion rules specified in an APIConversion are correct.
func (o *apiConversionAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != apisv1alpha1.Resource("apiconversions") {
		return nil
	}

	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return admission.NewForbidden(a, fmt.Errorf("error determining workspace: %w", err))
	}

	if cluster.Name == apibinding.SystemBoundCRDsClusterName {
		// TODO(ncdc): do we also want to validate these conversions? They've already been validated once (in their
		// original logical cluster).
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	apiResourceSchema, err := o.getAPIResourceSchema(cluster.Name, u.GetName())
	if err != nil {
		return admission.NewForbidden(a, fmt.Errorf("error getting APIResourceSchema %s: %w", u.GetName(), err))
	}

	apiConversion := &apisv1alpha1.APIConversion{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, apiConversion); err != nil {
		return fmt.Errorf("failed to convert unstructured to APIConversion: %w", err)
	}

	structuralSchemas := map[string]*structuralschema.Structural{}
	for i, v := range apiResourceSchema.Spec.Versions {
		schema, err := v.GetSchema()
		if err != nil {
			return admission.NewForbidden(a, field.Required(field.NewPath("spec", "versions").Index(i).Child("schema"), "is required"))
		}

		internalJSONSchemaProps := &apiextensionsinternal.JSONSchemaProps{}
		if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(schema, internalJSONSchemaProps, nil); err != nil {
			return fmt.Errorf("failed converting version %s validation to internal version: %w", v.Name, err)
		}

		structuralSchema, err := structuralschema.NewStructural(internalJSONSchemaProps)
		if err != nil {
			return fmt.Errorf("error getting structural schema for version %s: %w", v.Name, err)
		}

		structuralSchemas[v.Name] = structuralSchema
	}

	if _, err := conversion.Compile(apiConversion, structuralSchemas); err != nil {
		return fmt.Errorf("error compiling conversion rules: %w", err)
	}

	return nil
}

// ValidateInitialization ensures the required injected fields are set.
func (o *apiConversionAdmission) ValidateInitialization() error {
	if o.getAPIResourceSchema == nil {
		return fmt.Errorf("getAPIResourceSchema is unset")
	}

	return nil
}

func (o *apiConversionAdmission) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	o.getAPIResourceSchema = func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
		apiResourceSchema, err := local.Apis().V1alpha1().APIResourceSchemas().Lister().Cluster(clusterName).Get(name)
		if err == nil {
			return apiResourceSchema, nil
		}
		return global.Apis().V1alpha1().APIResourceSchemas().Lister().Cluster(clusterName).Get(name)
	}
}
