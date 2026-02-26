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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/endpointslice"
)

// TODO(gman0): consider adding caching for already discovered types.

type virtualStorageVerbsProvider struct {
	resourceName  string
	resourceVerbs map[string][]string // (Sub-)Resource -> Verbs
}

type virtualStorageClientOptions struct {
	// Client configuration.
	VWClientConfig *rest.Config

	// Local shard info.
	ThisShardName                      shard.Name
	ThisShardVirtualWorkspaceURLGetter func() string

	// Misc.
	GetUnstructuredEndpointSlice    func(ctx context.Context, cluster logicalcluster.Name, shard shard.Name, gvr schema.GroupVersionResource, name string) (*unstructured.Unstructured, error)
	APIResourceDiscoveryForResource func(ctx context.Context, cfg *rest.Config, groupVersion schema.GroupVersion) (*metav1.APIResourceList, error)
	RESTMappingFor                  func(cluster logicalcluster.Name, gk schema.GroupKind) (*meta.RESTMapping, error)
}

func newVirtualStorageVerbsProvider(
	ctx context.Context,

	vrResource schema.GroupVersionResource,
	vrIdentity string,

	apiExport *apisv1alpha2.APIExport,
	opts *virtualStorageClientOptions,
) (*virtualStorageVerbsProvider, error) {
	var virtualStorage *apisv1alpha2.ResourceSchemaStorageVirtual
	for _, resourceSchema := range apiExport.Spec.Resources {
		if resourceSchema.Storage.Virtual != nil &&
			resourceSchema.Storage.Virtual.IdentityHash == vrIdentity &&
			resourceSchema.Group == vrResource.Group &&
			resourceSchema.Name == vrResource.Resource {
			virtualStorage = resourceSchema.Storage.Virtual
			break
		}
	}
	if virtualStorage == nil {
		return nil, fmt.Errorf("no APIExports for virtual resource %s with identity %s", vrResource, vrIdentity)
	}

	sliceKind := schema.GroupKind{
		Group: ptr.Deref(virtualStorage.Reference.APIGroup, ""),
		Kind:  virtualStorage.Reference.Kind,
	}

	// TODO(gman0): consider skipping real discovery for known virtual resources
	// once we stabilize this feature and have proper testing for it.
	//
	// Known virtual resource types:
	/*switch sliceKind {
	case schema.GroupKind{
		Group: "cache.kcp.io",
		Kind:  "CachedResourceEndpointSlice",
	}:
		return &virtualCachedResourceVerbsProvider{}, nil
	}*/

	// Otherwise we need the VW url from an endpoint slice, where we
	// can forward the discovery request to and find out the verbs.

	apiExportShard := shard.Name(apiExport.Annotations[shard.AnnotationKey])
	if apiExportShard.Empty() {
		apiExportShard = opts.ThisShardName
	}
	vrEndpointURL, err := getVirtualResourceURL(
		ctx,
		logicalcluster.From(apiExport),
		apiExportShard,
		sliceKind,
		virtualStorage.Reference.Name,
		opts,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve virtual workspace URL: %v", err)
	}

	vwCfg := rest.CopyConfig(opts.VWClientConfig)
	vwCfg.Host = vrEndpointURL

	apiResource, err := opts.APIResourceDiscoveryForResource(ctx, vwCfg, vrResource.GroupVersion())
	if err != nil {
		return nil, fmt.Errorf("failed to perform API discovery: %v", err)
	}

	return &virtualStorageVerbsProvider{
		resourceName:  vrResource.Resource,
		resourceVerbs: buildVerbsMap(apiResource, vrResource.Resource),
	}, nil
}

func (p *virtualStorageVerbsProvider) resource() []string {
	return p.resourceVerbs[p.resourceName]
}

func (p *virtualStorageVerbsProvider) statusSubresource() []string {
	return p.resourceVerbs[p.resourceName+"/status"]
}

func (p *virtualStorageVerbsProvider) scaleSubresource() []string {
	return p.resourceVerbs[p.resourceName+"/scale"]
}

func buildVerbsMap(apiResources *metav1.APIResourceList, resourceName string) map[string][]string {
	m := make(map[string][]string)
	for _, apiResource := range apiResources.APIResources {
		if apiResource.Name == resourceName || strings.HasPrefix(apiResource.Name, resourceName+"/") {
			m[apiResource.Name] = apiResource.Verbs
		}
	}
	return m
}

func getVirtualResourceURL(
	ctx context.Context,
	apiExportCluster logicalcluster.Name,
	apiExportShard shard.Name,
	sliceKind schema.GroupKind,
	sliceName string,
	opts *virtualStorageClientOptions,
) (string, error) {
	sliceMapping, err := opts.RESTMappingFor(apiExportCluster, sliceKind)
	if err != nil {
		return "", err
	}

	slice, err := opts.GetUnstructuredEndpointSlice(ctx, apiExportCluster, apiExportShard, schema.GroupVersionResource{
		Group:    sliceMapping.Resource.Group,
		Version:  sliceMapping.Resource.Version,
		Resource: sliceMapping.Resource.Resource,
	}, sliceName)
	if err != nil {
		return "", err
	}

	urls, err := endpointslice.ListURLsFromUnstructured(*slice)
	if err != nil {
		return "", err
	}

	return endpointslice.FindOneURL(opts.ThisShardVirtualWorkspaceURLGetter(), urls)
}
