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
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/conversion"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
)

// CRConverterFactory instantiates converters that are capable of converting custom resources between different API
// versions. It supports CEL-based conversion rules from APIConversion resources and the "none" conversion strategy.
type CRConverterFactory struct {
	getAPIConversion                func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIConversion, error)
	objectCELTransformationsTimeout time.Duration
}

var _ conversion.Factory = &CRConverterFactory{}

// NewCRConverterFactory returns a CRConverterFactory that supports APIConversion-based conversions and the "none"
// conversion strategy.
func NewCRConverterFactory(
	apiConversionInformer apisinformers.APIConversionClusterInformer,
	objectCELTransformationsTimeout time.Duration,
) *CRConverterFactory {
	return &CRConverterFactory{
		getAPIConversion: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIConversion, error) {
			return apiConversionInformer.Lister().Cluster(clusterName).Get(name)
		},
		objectCELTransformationsTimeout: objectCELTransformationsTimeout,
	}
}

// NewConverter returns the appropriate conversion.Converter based on the CRD. If the CRD identifies as for a "wildcard
// partial metadata request", the nop converter is used. Otherwise, it returns a CEL-based converter if there is an
// associated APIConversion, a nop converter if the strategy is "none", or an error otherwise.
func (f *CRConverterFactory) NewConverter(crd *apiextensionsv1.CustomResourceDefinition) (conversion.CRConverter, error) {
	// Wildcard, partial metadata requests never need conversion
	if strings.HasSuffix(string(crd.UID), ".wildcard.partial-metadata") {
		return conversion.NewNOPConverter(), nil
	}

	// We have to return a deferredConverter here instead of an actual one because apiextensions-apiserver creates the
	// converter once, when it creates serving info for a CRD. Because we aren't currently guaranteeing order (or cache
	// freshness), there is always a chance that serving info is created for a CRD before an associated APIConversion
	// exists and is present in the informer cache.
	return &deferredConverter{
		crd: crd,

		getAPIConversion: f.getAPIConversion,
		newConverter: func(crd *apiextensionsv1.CustomResourceDefinition, apiConversion *apisv1alpha1.APIConversion) (conversion.CRConverter, error) {
			return NewConverter(crd, apiConversion, f.objectCELTransformationsTimeout)
		},
	}, nil
}
