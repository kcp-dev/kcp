/*
Copyright 2025 The kcp Authors.

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

package aggregatingcrdversiondiscovery

import (
	"context"
	"fmt"
	"strings"

	apiextensionshelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

const (
	boundCRDVirtualStorageAnnotationPrefix = "virtual:"
)

// resourceVerbsProvider provides verbs for a given virtual (sub)resource.
type resourceVerbsProvider interface {
	resource() []string
	statusSubresource() []string
	scaleSubresource() []string
}

type storageAwareResourceVerbsProviderFactory struct {
	*virtualStorageClientOptions

	getAPIExportByPath                     func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getAPIExportsByVirtualResourceIdentity func(vrIdentity string) ([]*apisv1alpha2.APIExport, error)
}

func (f *storageAwareResourceVerbsProviderFactory) newResourceVerbsProvider(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition, requestedVersion string) (resourceVerbsProvider, error) {
	if crd.Annotations[apisv1alpha1.AnnotationSchemaStorageKey] == "" {
		// Without apis.kcp.io/schema-storage annotation on the CRD we assume it uses the standard CRD storage.
		return &crdStorageVerbsProvider{
			terminating: apiextensionshelpers.IsCRDConditionTrue(crd, apiextensionsv1.Terminating),
		}, nil
	}

	//  Otherwise we need to check what resource storage is in the export, if one exists.

	if strings.HasPrefix(crd.Annotations[apisv1alpha1.AnnotationSchemaStorageKey], boundCRDVirtualStorageAnnotationPrefix) {
		// Resources with virtual storage need the ResourceSchemaStorageVirtual from their parent APIExport.

		vrIdentity := strings.TrimPrefix(crd.Annotations[apisv1alpha1.AnnotationSchemaStorageKey], boundCRDVirtualStorageAnnotationPrefix)
		apiExportIdentity := crd.Annotations[apisv1alpha1.AnnotationAPIIdentityKey]

		apiExports, err := f.getAPIExportsByVirtualResourceIdentity(vrIdentity)
		if err != nil {
			return nil, err
		}
		var apiExport *apisv1alpha2.APIExport
		for _, ae := range apiExports {
			if ae.Status.IdentityHash == apiExportIdentity {
				apiExport = ae
				break
			}
		}
		if apiExport == nil {
			return nil, fmt.Errorf("no matching APIExport for identity %s and virtual resource identity %s", apiExportIdentity, vrIdentity)
		}

		gvr := schema.GroupVersionResource{
			Group:    crd.Spec.Group,
			Version:  requestedVersion,
			Resource: crd.Status.AcceptedNames.Plural,
		}

		return newVirtualStorageVerbsProvider(ctx, gvr, vrIdentity, apiExport, f.virtualStorageClientOptions)
	}

	// We don't support any non-CRD storages other than virtual.
	return nil, fmt.Errorf("unknown %s annotation %q on bound CRD %s", apisv1alpha1.AnnotationSchemaStorageKey, crd.Annotations[apisv1alpha1.AnnotationSchemaStorageKey], crd.Name)
}
