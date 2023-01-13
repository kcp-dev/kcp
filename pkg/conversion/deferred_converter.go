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

package conversion

import (
	"fmt"
	"sync"

	"github.com/kcp-dev/logicalcluster/v3"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/conversion"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

// deferredConverter implements conversion.Converter and determines on the fly what type of converter to use.
type deferredConverter struct {
	crd *apiextensionsv1.CustomResourceDefinition

	// lock guards delegate
	lock sync.Mutex
	// delegate is the actual converter
	delegate conversion.CRConverter

	getAPIConversion func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIConversion, error)
	newConverter     func(crd *apiextensionsv1.CustomResourceDefinition, apiConversion *apisv1alpha1.APIConversion) (conversion.CRConverter, error)
}

// Convert converts in to targetGV. If there is an APIConversion for this CRD, a CEL-based converter is used. Otherwise,
// a nop converter is used.
func (f *deferredConverter) Convert(in *unstructured.UnstructuredList, targetGV schema.GroupVersion) (*unstructured.UnstructuredList, error) {
	converter, err := f.getConverter()
	if err != nil {
		return nil, fmt.Errorf("error getting converter: %w", err)
	}

	return converter.Convert(in, targetGV)
}

func (f *deferredConverter) getConverter() (conversion.CRConverter, error) {
	f.lock.Lock()
	defer f.lock.Unlock()

	if f.delegate != nil {
		return f.delegate, nil
	}

	var clusterName logicalcluster.Name
	var conversionName string
	_, boundCRD := f.crd.Annotations[apisv1alpha1.AnnotationBoundCRDKey]
	clusterNameAnnotation := f.crd.Annotations[apisv1alpha1.AnnotationSchemaClusterKey]
	schemaNameAnnotation := f.crd.Annotations[apisv1alpha1.AnnotationSchemaNameKey]

	switch {
	case boundCRD && clusterNameAnnotation != "" && schemaNameAnnotation != "":
		// If this is a bound CRD, we need to look up the APIConversion in the workspace where the APIResourceSchema
		// lives.
		clusterName = logicalcluster.Name(clusterNameAnnotation)
		conversionName = schemaNameAnnotation
	case !boundCRD:
		// Otherwise, it has to be in the same place as the CRD
		clusterName = logicalcluster.From(f.crd)
		conversionName = f.crd.Name
	default:
		logging.WithObject(klog.Background(), f.crd).Error(nil, "unable to determine APIConversion name",
			"boundCRD", boundCRD,
			"crdSchemaClusterNameAnnotation", clusterNameAnnotation,
			"crdSchemaNameAnnotation", schemaNameAnnotation,
		)
		return nil, fmt.Errorf("unable to get converter")
	}

	apiConversion, err := f.getAPIConversion(clusterName, conversionName)
	if err != nil && !errors.IsNotFound(err) {
		return nil, fmt.Errorf("error checking for APIConversion for CRD %s|%s: %w", clusterNameAnnotation, f.crd.Name, err)
	}
	if errors.IsNotFound(err) {
		switch f.crd.Spec.Conversion.Strategy {
		case apiextensionsv1.NoneConverter:
			return conversion.NewNOPConverter(), nil
		case apiextensionsv1.WebhookConverter:
			return nil, fmt.Errorf("conversion strategy %q is not supported for CRD %s", f.crd.Spec.Conversion.Strategy, f.crd.Name)
		default:
			return nil, fmt.Errorf("unknown conversion strategy %q for CRD %s", f.crd.Spec.Conversion.Strategy, f.crd.Name)
		}
	}

	converter, err := f.newConverter(f.crd, apiConversion)
	if err != nil {
		return nil, fmt.Errorf("error creating converter for CRD %s|%s: %w", clusterNameAnnotation, f.crd.Name, err)
	}
	f.delegate = converter

	return converter, nil
}
