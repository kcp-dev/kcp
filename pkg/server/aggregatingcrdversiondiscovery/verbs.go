/*
Copyright 2025 The KCP Authors.

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
	"fmt"
	"strings"

	apiextensionshelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

const (
	boundCRDVirtualStorageAnnotationPrefix = "virtual:"
)

// resourceVerbsProvider provides verbs for a given virtual (sub)resource.
type resourceVerbsProvider interface {
	resource(crd *apiextensionsv1.CustomResourceDefinition) (verbs []string, err error)
	statusSubresource(crd *apiextensionsv1.CustomResourceDefinition) (verbs []string, err error)
	scaleSubresource(crd *apiextensionsv1.CustomResourceDefinition) (verbs []string, err error)
}

// storageAwareResourceVerbsProvider is able to list supported verbs of a resource based on its storage
// definition (.spec.resources[].storage) in its APIExport (if any), or fall-back to regular CRD verbs.
type storageAwareResourceVerbsProvider struct {
	getAPIExportByPath                     func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getAPIExportsByVirtualResourceIdentity func(vrIdentity string) ([]*apisv1alpha2.APIExport, error)

	// These maps map <Endpoint slice kind>.<Endpoint slice group> to the set of verbs to be
	// advertised for that virtual resource. They are in a role of a cache: we know these verbs
	// won't change, and so we don't have to do discovery against the backing virtual workspace
	// each time we receive a request.
	knownVirtualResourceVerbs       map[string][]string
	knownVirtualResourceStatusVerbs map[string][]string
	knownVirtualResourceScaleVerbs  map[string][]string
}

func (p *storageAwareResourceVerbsProvider) getVirtualResourceStorage(apiExportIdentity, vrIdentity string, gr schema.GroupResource) (*apisv1alpha2.ResourceSchemaStorageVirtual, *apisv1alpha2.APIExport, error) {
	apiExports, err := p.getAPIExportsByVirtualResourceIdentity(vrIdentity)
	if err != nil {
		return nil, nil, err
	}
	if len(apiExports) == 0 {
		return nil, nil, fmt.Errorf("no APIExports for virtual resource identity %s", vrIdentity)
	}

	var apiExport *apisv1alpha2.APIExport
	for _, ae := range apiExports {
		if ae.Status.IdentityHash == apiExportIdentity {
			apiExport = ae
			break
		}
	}
	if apiExport == nil {
		return nil, nil, fmt.Errorf("no matching APIExport for identity %s and virtual resource identity %s", apiExportIdentity, vrIdentity)
	}

	var virtualStorage *apisv1alpha2.ResourceSchemaStorageVirtual
	for _, resourceSchema := range apiExport.Spec.Resources {
		if resourceSchema.Storage.Virtual != nil &&
			resourceSchema.Storage.Virtual.IdentityHash == vrIdentity &&
			resourceSchema.Group == gr.Group &&
			resourceSchema.Name == gr.Resource {
			virtualStorage = resourceSchema.Storage.Virtual
			break
		}
	}

	if virtualStorage == nil {
		return nil, nil, fmt.Errorf("no APIExports for virtual resource %s with identity %s", gr, vrIdentity)
	}

	return virtualStorage, apiExport, nil
}

func (p *storageAwareResourceVerbsProvider) tryVirtualStorageVerbs(crd *apiextensionsv1.CustomResourceDefinition, verbsMap map[string][]string) ([]string, error) {
	if crd.Annotations[apisv1alpha1.AnnotationSchemaStorageKey] != "" {
		if !strings.HasPrefix(crd.Annotations[apisv1alpha1.AnnotationSchemaStorageKey], boundCRDVirtualStorageAnnotationPrefix) {
			// We don't support any non-CRD storages other than virtual.
			return nil, fmt.Errorf("unknown %s annotation %q on bound CRD %s", apisv1alpha1.AnnotationSchemaStorageKey, crd.Annotations[apisv1alpha1.AnnotationSchemaStorageKey], crd.Name)
		}

		vrIdentity := strings.TrimPrefix(crd.Annotations[apisv1alpha1.AnnotationSchemaStorageKey], boundCRDVirtualStorageAnnotationPrefix)
		virtualStorage, apiExport, err := p.getVirtualResourceStorage(
			crd.Annotations[apisv1alpha1.AnnotationAPIIdentityKey],
			vrIdentity,
			schema.GroupResource{
				Group:    crd.Spec.Group,
				Resource: crd.Status.AcceptedNames.Plural,
			},
		)
		if err != nil {
			return nil, err
		}

		// Check against known virtual resources.
		if verbs, ok := verbsMap[fmt.Sprintf("%s.%s", virtualStorage.Reference.Kind, ptr.Deref(virtualStorage.Reference.APIGroup, "core"))]; ok {
			return verbs, nil
		}

		// TODO(gman0): add a fallback option to retrieve verbs for unknown/dynamically added VRs with real
		// discovery from their VWs, if we ever need such a thing. For now we're just returning an error.
		return nil, fmt.Errorf("unknown virtual resource endpoint slice %s.%s defined in %s|%s", virtualStorage.Reference.Kind, ptr.Deref(virtualStorage.Reference.APIGroup, "core"), logicalcluster.From(apiExport), apiExport.Name)
	}

	return nil, nil
}

func (p *storageAwareResourceVerbsProvider) resource(crd *apiextensionsv1.CustomResourceDefinition) ([]string, error) {
	if virtualStorageVerbs, err := p.tryVirtualStorageVerbs(crd, p.knownVirtualResourceVerbs); err != nil {
		return nil, err
	} else if virtualStorageVerbs != nil {
		return virtualStorageVerbs, nil
	}

	// Resources with CRD storage get regular CRD verbs.

	verbs := metav1.Verbs([]string{"delete", "deletecollection", "get", "list", "patch", "create", "update", "watch"})
	// if we're terminating we don't allow some verbs
	if apiextensionshelpers.IsCRDConditionTrue(crd, apiextensionsv1.Terminating) {
		verbs = metav1.Verbs([]string{"delete", "deletecollection", "get", "list", "watch"})
	}

	return verbs, nil
}

func (p *storageAwareResourceVerbsProvider) statusSubresource(crd *apiextensionsv1.CustomResourceDefinition) ([]string, error) {
	if virtualStorageVerbs, err := p.tryVirtualStorageVerbs(crd, p.knownVirtualResourceStatusVerbs); err != nil {
		return nil, err
	} else if virtualStorageVerbs != nil {
		return virtualStorageVerbs, nil
	}

	// Resources with CRD storage get regular CRD status verbs.
	return metav1.Verbs([]string{"get", "patch", "update"}), nil
}

func (p *storageAwareResourceVerbsProvider) scaleSubresource(crd *apiextensionsv1.CustomResourceDefinition) ([]string, error) {
	if virtualStorageVerbs, err := p.tryVirtualStorageVerbs(crd, p.knownVirtualResourceStatusVerbs); err != nil {
		return nil, err
	} else if virtualStorageVerbs != nil {
		return virtualStorageVerbs, nil
	}

	// Resources with CRD storage get regular CRD scale verbs.
	return metav1.Verbs([]string{"get", "patch", "update"}), nil
}
