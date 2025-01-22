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

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	applyconfigurationapisv1alpha1 "github.com/kcp-dev/kcp/sdk/client/applyconfiguration/apis/v1alpha1"
	scheme "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/scheme"
)

// APIExportEndpointSlicesGetter has a method to return a APIExportEndpointSliceInterface.
// A group's client should implement this interface.
type APIExportEndpointSlicesGetter interface {
	APIExportEndpointSlices() APIExportEndpointSliceInterface
}

// APIExportEndpointSliceInterface has methods to work with APIExportEndpointSlice resources.
type APIExportEndpointSliceInterface interface {
	Create(ctx context.Context, aPIExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice, opts v1.CreateOptions) (*apisv1alpha1.APIExportEndpointSlice, error)
	Update(ctx context.Context, aPIExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice, opts v1.UpdateOptions) (*apisv1alpha1.APIExportEndpointSlice, error)
	// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
	UpdateStatus(ctx context.Context, aPIExportEndpointSlice *apisv1alpha1.APIExportEndpointSlice, opts v1.UpdateOptions) (*apisv1alpha1.APIExportEndpointSlice, error)
	Delete(ctx context.Context, name string, opts v1.DeleteOptions) error
	DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error
	Get(ctx context.Context, name string, opts v1.GetOptions) (*apisv1alpha1.APIExportEndpointSlice, error)
	List(ctx context.Context, opts v1.ListOptions) (*apisv1alpha1.APIExportEndpointSliceList, error)
	Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error)
	Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *apisv1alpha1.APIExportEndpointSlice, err error)
	Apply(ctx context.Context, aPIExportEndpointSlice *applyconfigurationapisv1alpha1.APIExportEndpointSliceApplyConfiguration, opts v1.ApplyOptions) (result *apisv1alpha1.APIExportEndpointSlice, err error)
	// Add a +genclient:noStatus comment above the type to avoid generating ApplyStatus().
	ApplyStatus(ctx context.Context, aPIExportEndpointSlice *applyconfigurationapisv1alpha1.APIExportEndpointSliceApplyConfiguration, opts v1.ApplyOptions) (result *apisv1alpha1.APIExportEndpointSlice, err error)
	APIExportEndpointSliceExpansion
}

// aPIExportEndpointSlices implements APIExportEndpointSliceInterface
type aPIExportEndpointSlices struct {
	*gentype.ClientWithListAndApply[*apisv1alpha1.APIExportEndpointSlice, *apisv1alpha1.APIExportEndpointSliceList, *applyconfigurationapisv1alpha1.APIExportEndpointSliceApplyConfiguration]
}

// newAPIExportEndpointSlices returns a APIExportEndpointSlices
func newAPIExportEndpointSlices(c *ApisV1alpha1Client) *aPIExportEndpointSlices {
	return &aPIExportEndpointSlices{
		gentype.NewClientWithListAndApply[*apisv1alpha1.APIExportEndpointSlice, *apisv1alpha1.APIExportEndpointSliceList, *applyconfigurationapisv1alpha1.APIExportEndpointSliceApplyConfiguration](
			"apiexportendpointslices",
			c.RESTClient(),
			scheme.ParameterCodec,
			"",
			func() *apisv1alpha1.APIExportEndpointSlice { return &apisv1alpha1.APIExportEndpointSlice{} },
			func() *apisv1alpha1.APIExportEndpointSliceList { return &apisv1alpha1.APIExportEndpointSliceList{} },
		),
	}
}
