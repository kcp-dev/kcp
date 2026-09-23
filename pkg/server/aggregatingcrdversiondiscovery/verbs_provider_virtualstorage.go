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
	"k8s.io/apimachinery/pkg/labels"
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
	ThisShardLabels                    func() labels.Set

	// Misc.
	GetUnstructuredEndpointSlice    func(ctx context.Context, cluster logicalcluster.Name, shard shard.Name, gvr schema.GroupVersionResource, name string) (*unstructured.Unstructured, error)
	APIResourceDiscoveryForResource func(ctx context.Context, cfg *rest.Config, groupVersion schema.GroupVersion) (*metav1.APIResourceList, error)
	RESTMappingFor                  func(cluster logicalcluster.Name, gk schema.GroupKind) (*meta.RESTMapping, error)
}

func newVirtualStorageVerbsProvider(
	ctx context.Context,

	vrResource schema.GroupVersionResource,
	apiExport *apisv1alpha2.APIExport,
	opts *virtualStorageClientOptions,
) (*virtualStorageVerbsProvider, error) {
	var virtualStorage *apisv1alpha2.ResourceSchemaStorageVirtual
	for _, resourceSchema := range apiExport.Spec.Resources {
		if resourceSchema.Storage.Virtual != nil &&
			resourceSchema.Group == vrResource.Group &&
			resourceSchema.Name == vrResource.Resource {
			virtualStorage = resourceSchema.Storage.Virtual
			break
		}
	}
	if virtualStorage == nil {
		return nil, fmt.Errorf("APIExport %s|%s doesn't export virtual resource %s", logicalcluster.From(apiExport), apiExport.Name, vrResource)
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
		Kind:  "ClusterCachedResourceEndpointSlice",
	}:
		return &virtualClusterCachedResourceVerbsProvider{}, nil
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

// subresources strips the "<resource>/" prefix that discovery uses and returns
// every sub-resource the virtual workspace advertised. buildVerbsMap already
// collects them all; until now only status and scale were read back out.
func (p *virtualStorageVerbsProvider) subresources() map[string][]string {
	out := map[string][]string{}

	for name, verbs := range p.resourceVerbs {
		sub, ok := strings.CutPrefix(name, p.resourceName+"/")
		if !ok || sub == "" {
			continue
		}
		out[sub] = verbs
	}

	return out
}

// compositeVerbsProvider answers for a resource whose parent and sub-resources are
// served by different things: a CRD-stored parent that carries custom sub-resources
// backed by a virtual workspace. The parent's verbs come from CRD storage, each
// sub-resource's from whichever provider serves it.
type compositeVerbsProvider struct {
	parent           resourceVerbsProvider
	subresourceVerbs map[string][]string
}

func (p *compositeVerbsProvider) resource() []string {
	return p.parent.resource()
}

func (p *compositeVerbsProvider) subresources() map[string][]string {
	out := map[string][]string{}

	// The parent contributes status and scale; the sub-resource providers contribute
	// the custom ones. A custom sub-resource may not be called status or scale, so
	// these cannot collide.
	for name, verbs := range p.parent.subresources() {
		out[name] = verbs
	}
	for name, verbs := range p.subresourceVerbs {
		out[name] = verbs
	}

	return out
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

// thisShardLabels reports the shard's labels, or none if the caller did not
// supply a way to read them. Without labels only endpoints that select shards
// are affected, and those fail with a clear error rather than silently.
func thisShardLabels(opts *virtualStorageClientOptions) labels.Set {
	if opts.ThisShardLabels == nil {
		return nil
	}
	return opts.ThisShardLabels()
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

	endpoints, err := endpointslice.ListEndpointsFromUnstructured(*slice)
	if err != nil {
		return "", err
	}

	return endpointslice.PickURL(opts.ThisShardVirtualWorkspaceURLGetter(), thisShardLabels(opts), endpoints)
}

// subresourceVerbsProvider answers for a single custom subresource, which names its
// own virtual workspace independently of the parent resource's storage.
type subresourceVerbsProvider struct {
	subresourceVerbs []string
}

func newSubresourceVerbsProvider(
	ctx context.Context,

	vrResource schema.GroupVersionResource,
	subresource string,
	virtual *apisv1alpha2.ResourceSchemaStorageVirtual,
	apiExport *apisv1alpha2.APIExport,
	opts *virtualStorageClientOptions,
) (*subresourceVerbsProvider, error) {
	sliceKind := schema.GroupKind{
		Group: ptr.Deref(virtual.Reference.APIGroup, ""),
		Kind:  virtual.Reference.Kind,
	}

	apiExportShard := shard.Name(apiExport.Annotations[shard.AnnotationKey])
	if apiExportShard.Empty() {
		apiExportShard = opts.ThisShardName
	}

	vrEndpointURL, err := getVirtualResourceURL(ctx, logicalcluster.From(apiExport), apiExportShard, sliceKind, virtual.Reference.Name, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve virtual workspace URL: %w", err)
	}

	vwCfg := rest.CopyConfig(opts.VWClientConfig)
	vwCfg.Host = vrEndpointURL

	apiResources, err := opts.APIResourceDiscoveryForResource(ctx, vwCfg, vrResource.GroupVersion())
	if err != nil {
		return nil, fmt.Errorf("failed to perform API discovery: %w", err)
	}

	// Ask the virtual workspace what it serves rather than trusting the declaration:
	// the APIExport says a subresource exists, the workspace says which verbs reach
	// it, and advertising a verb the workspace will refuse helps nobody.
	return &subresourceVerbsProvider{
		subresourceVerbs: buildVerbsMap(apiResources, vrResource.Resource)[vrResource.Resource+"/"+subresource],
	}, nil
}

func (p *subresourceVerbsProvider) verbs() []string {
	return p.subresourceVerbs
}
