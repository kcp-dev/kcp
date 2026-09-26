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
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

const (
	boundCRDVirtualStorageAnnotationPrefix = "virtual:"
)

// resourceVerbsProvider provides verbs for a given (sub)resource.
type resourceVerbsProvider interface {
	resource() []string

	// subresources maps a bare sub-resource name ("status", "scale", "ssh") to the
	// verbs it serves. status and scale are still gated by the caller on the bound
	// CRD's spec, because a provider may report verbs for a sub-resource the
	// resource does not actually declare. Custom sub-resources are reported only
	// when the APIExport declares them, so they are emitted as they come.
	subresources() map[string][]string
}

type storageAwareResourceVerbsProviderFactory struct {
	*virtualStorageClientOptions

	getAPIExportByPath                        func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getAPIExportsByVirtualResourceFingerprint func(fingerprint string) ([]*apisv1alpha2.APIExport, error)
}

func (f *storageAwareResourceVerbsProviderFactory) newResourceVerbsProvider(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition, requestedVersion string) (resourceVerbsProvider, error) {
	gvr := schema.GroupVersionResource{
		Group:    crd.Spec.Group,
		Version:  requestedVersion,
		Resource: crd.Status.AcceptedNames.Plural,
	}

	if crd.Annotations[apisv1alpha1.AnnotationSchemaStorageKey] == "" {
		// Without apis.kcp.io/schema-storage annotation on the CRD we assume it uses the standard CRD storage.
		parent := &crdStorageVerbsProvider{
			terminating: apiextensionshelpers.IsCRDConditionTrue(crd, apiextensionsv1.Terminating),
		}

		// The parent is stored in a CRD, but it may still carry custom subresources
		// served elsewhere. Those verbs have to be fetched from each subresource's
		// own virtual workspace and merged on top of the CRD ones.
		subresourceVerbs, err := f.customSubresourceVerbs(ctx, crd, gvr)
		if err != nil {
			return nil, err
		}
		if len(subresourceVerbs) == 0 {
			return parent, nil
		}

		return &compositeVerbsProvider{parent: parent, subresourceVerbs: subresourceVerbs}, nil
	}

	//  Otherwise we need to check what resource storage is in the export, if one exists.

	if strings.HasPrefix(crd.Annotations[apisv1alpha1.AnnotationSchemaStorageKey], boundCRDVirtualStorageAnnotationPrefix) {
		// Resources with virtual storage need the ResourceSchemaStorageVirtual from their parent APIExport.

		fingerprint := strings.TrimPrefix(crd.Annotations[apisv1alpha1.AnnotationSchemaStorageKey], boundCRDVirtualStorageAnnotationPrefix)
		apiExports, err := f.getAPIExportsByVirtualResourceFingerprint(fingerprint)
		if err != nil {
			return nil, err
		}
		if len(apiExports) == 0 {
			return nil, fmt.Errorf("no matching APIExport for virtual resource fingerprint %q", fingerprint)
		}
		// A fingerprint carries the owning APIExport's logical cluster and name, so it
		// identifies exactly one APIExport. More than one match means the index is
		// keyed by something weaker than we think, and picking either one would serve
		// another workspace's virtual workspace URL under this one's discovery.
		if len(apiExports) > 1 {
			return nil, fmt.Errorf("virtual resource fingerprint %q matches %d APIExports, expected exactly one", fingerprint, len(apiExports))
		}

		return newVirtualStorageVerbsProvider(ctx, gvr, apiExports[0], f.virtualStorageClientOptions)
	}

	// We don't support any non-CRD storages other than virtual.
	return nil, fmt.Errorf("unknown %s annotation %q on bound CRD %s", apisv1alpha1.AnnotationSchemaStorageKey, crd.Annotations[apisv1alpha1.AnnotationSchemaStorageKey], crd.Name)
}

// customSubresourceVerbs resolves the verbs of every custom subresource the
// declaring APIExport lists for this resource, by asking each subresource's own
// virtual workspace what it serves.
//
// A subresource that cannot be resolved is omitted rather than failing the whole
// discovery document: one unreachable virtual workspace should cost its own
// subresource, not every resource in the group.
func (f *storageAwareResourceVerbsProviderFactory) customSubresourceVerbs(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition, gvr schema.GroupVersionResource) (map[string][]string, error) {
	ref := crd.Annotations[apisv1alpha1.AnnotationSubresourcesKey]
	if ref == "" {
		return nil, nil
	}

	clusterName, exportName, ok := strings.Cut(ref, "|")
	if !ok || clusterName == "" || exportName == "" {
		return nil, fmt.Errorf("malformed %s annotation %q on bound CRD %s", apisv1alpha1.AnnotationSubresourcesKey, ref, crd.Name)
	}

	apiExport, err := f.getAPIExportByPath(logicalcluster.NewPath(clusterName), exportName)
	if err != nil {
		return nil, err
	}

	out := map[string][]string{}
	for i := range apiExport.Spec.Resources {
		declared := &apiExport.Spec.Resources[i]
		if declared.Group != gvr.Group || !declared.IsSubresource() {
			continue
		}
		resource, subresource := declared.SplitName()
		if resource != gvr.Resource || declared.Storage.Virtual == nil {
			continue
		}

		provider, err := newSubresourceVerbsProvider(ctx, gvr, subresource, declared.Storage.Virtual, apiExport, f.virtualStorageClientOptions)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("failed to resolve verbs for subresource %s/%s: %w", gvr.Resource, subresource, err))
			continue
		}
		if verbs := provider.verbs(); len(verbs) > 0 {
			out[subresource] = verbs
		}
	}

	return out, nil
}
