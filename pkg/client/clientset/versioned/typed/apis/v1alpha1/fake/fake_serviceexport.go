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

// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	"context"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	schema "k8s.io/apimachinery/pkg/runtime/schema"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"

	v1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

// FakeServiceExports implements ServiceExportInterface
type FakeServiceExports struct {
	Fake *FakeApisV1alpha1
}

var serviceexportsResource = schema.GroupVersionResource{Group: "apis.kcp.dev", Version: "v1alpha1", Resource: "serviceexports"}

var serviceexportsKind = schema.GroupVersionKind{Group: "apis.kcp.dev", Version: "v1alpha1", Kind: "ServiceExport"}

// Get takes name of the serviceExport, and returns the corresponding serviceExport object, and an error if there is any.
func (c *FakeServiceExports) Get(ctx context.Context, name string, options v1.GetOptions) (result *v1alpha1.ServiceExport, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewRootGetAction(serviceexportsResource, name), &v1alpha1.ServiceExport{})
	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.ServiceExport), err
}

// List takes label and field selectors, and returns the list of ServiceExports that match those selectors.
func (c *FakeServiceExports) List(ctx context.Context, opts v1.ListOptions) (result *v1alpha1.ServiceExportList, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewRootListAction(serviceexportsResource, serviceexportsKind, opts), &v1alpha1.ServiceExportList{})
	if obj == nil {
		return nil, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1alpha1.ServiceExportList{ListMeta: obj.(*v1alpha1.ServiceExportList).ListMeta}
	for _, item := range obj.(*v1alpha1.ServiceExportList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested serviceExports.
func (c *FakeServiceExports) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewRootWatchAction(serviceexportsResource, opts))
}

// Create takes the representation of a serviceExport and creates it.  Returns the server's representation of the serviceExport, and an error, if there is any.
func (c *FakeServiceExports) Create(ctx context.Context, serviceExport *v1alpha1.ServiceExport, opts v1.CreateOptions) (result *v1alpha1.ServiceExport, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewRootCreateAction(serviceexportsResource, serviceExport), &v1alpha1.ServiceExport{})
	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.ServiceExport), err
}

// Update takes the representation of a serviceExport and updates it. Returns the server's representation of the serviceExport, and an error, if there is any.
func (c *FakeServiceExports) Update(ctx context.Context, serviceExport *v1alpha1.ServiceExport, opts v1.UpdateOptions) (result *v1alpha1.ServiceExport, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewRootUpdateAction(serviceexportsResource, serviceExport), &v1alpha1.ServiceExport{})
	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.ServiceExport), err
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *FakeServiceExports) UpdateStatus(ctx context.Context, serviceExport *v1alpha1.ServiceExport, opts v1.UpdateOptions) (*v1alpha1.ServiceExport, error) {
	obj, err := c.Fake.
		Invokes(testing.NewRootUpdateSubresourceAction(serviceexportsResource, "status", serviceExport), &v1alpha1.ServiceExport{})
	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.ServiceExport), err
}

// Delete takes name of the serviceExport and deletes it. Returns an error if one occurs.
func (c *FakeServiceExports) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewRootDeleteAction(serviceexportsResource, name), &v1alpha1.ServiceExport{})
	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakeServiceExports) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	action := testing.NewRootDeleteCollectionAction(serviceexportsResource, listOpts)

	_, err := c.Fake.Invokes(action, &v1alpha1.ServiceExportList{})
	return err
}

// Patch applies the patch and returns the patched serviceExport.
func (c *FakeServiceExports) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1alpha1.ServiceExport, err error) {
	obj, err := c.Fake.
		Invokes(testing.NewRootPatchSubresourceAction(serviceexportsResource, name, pt, data, subresources...), &v1alpha1.ServiceExport{})
	if obj == nil {
		return nil, err
	}
	return obj.(*v1alpha1.ServiceExport), err
}
