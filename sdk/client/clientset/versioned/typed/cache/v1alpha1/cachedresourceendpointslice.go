/*
Copyright The KCP Authors.

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

// Code generated by client-gen. DO NOT EDIT.

package v1alpha1

import (
	context "context"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	gentype "k8s.io/client-go/gentype"

	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	applyconfigurationcachev1alpha1 "github.com/kcp-dev/kcp/sdk/client/applyconfiguration/cache/v1alpha1"
	scheme "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/scheme"
)

// CachedResourceEndpointSlicesGetter has a method to return a CachedResourceEndpointSliceInterface.
// A group's client should implement this interface.
type CachedResourceEndpointSlicesGetter interface {
	CachedResourceEndpointSlices() CachedResourceEndpointSliceInterface
}

// CachedResourceEndpointSliceInterface has methods to work with CachedResourceEndpointSlice resources.
type CachedResourceEndpointSliceInterface interface {
	Create(ctx context.Context, cachedResourceEndpointSlice *cachev1alpha1.CachedResourceEndpointSlice, opts v1.CreateOptions) (*cachev1alpha1.CachedResourceEndpointSlice, error)
	Update(ctx context.Context, cachedResourceEndpointSlice *cachev1alpha1.CachedResourceEndpointSlice, opts v1.UpdateOptions) (*cachev1alpha1.CachedResourceEndpointSlice, error)
	// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
	UpdateStatus(ctx context.Context, cachedResourceEndpointSlice *cachev1alpha1.CachedResourceEndpointSlice, opts v1.UpdateOptions) (*cachev1alpha1.CachedResourceEndpointSlice, error)
	Delete(ctx context.Context, name string, opts v1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error
	Get(ctx context.Context, name string, opts v1.GetOptions) (*cachev1alpha1.CachedResourceEndpointSlice, error)
	List(ctx context.Context, opts v1.ListOptions) (*cachev1alpha1.CachedResourceEndpointSliceList, error)
	Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *cachev1alpha1.CachedResourceEndpointSlice, err error)
	Apply(ctx context.Context, cachedResourceEndpointSlice *applyconfigurationcachev1alpha1.CachedResourceEndpointSliceApplyConfiguration, opts v1.ApplyOptions) (result *cachev1alpha1.CachedResourceEndpointSlice, err error)
	// Add a +genclient:noStatus comment above the type to avoid generating ApplyStatus().
	ApplyStatus(ctx context.Context, cachedResourceEndpointSlice *applyconfigurationcachev1alpha1.CachedResourceEndpointSliceApplyConfiguration, opts v1.ApplyOptions) (result *cachev1alpha1.CachedResourceEndpointSlice, err error)
	CachedResourceEndpointSliceExpansion
}

// cachedResourceEndpointSlices implements CachedResourceEndpointSliceInterface
type cachedResourceEndpointSlices struct {
	*gentype.ClientWithListAndApply[*cachev1alpha1.CachedResourceEndpointSlice, *cachev1alpha1.CachedResourceEndpointSliceList, *applyconfigurationcachev1alpha1.CachedResourceEndpointSliceApplyConfiguration]
}

// newCachedResourceEndpointSlices returns a CachedResourceEndpointSlices
func newCachedResourceEndpointSlices(c *CacheV1alpha1Client) *cachedResourceEndpointSlices {
	return &cachedResourceEndpointSlices{
		gentype.NewClientWithListAndApply[*cachev1alpha1.CachedResourceEndpointSlice, *cachev1alpha1.CachedResourceEndpointSliceList, *applyconfigurationcachev1alpha1.CachedResourceEndpointSliceApplyConfiguration](
			"cachedresourceendpointslices",
			c.RESTClient(),
			scheme.ParameterCodec,
			"",
			func() *cachev1alpha1.CachedResourceEndpointSlice { return &cachev1alpha1.CachedResourceEndpointSlice{} },
			func() *cachev1alpha1.CachedResourceEndpointSliceList {
				return &cachev1alpha1.CachedResourceEndpointSliceList{}
			},
		),
	}
}
