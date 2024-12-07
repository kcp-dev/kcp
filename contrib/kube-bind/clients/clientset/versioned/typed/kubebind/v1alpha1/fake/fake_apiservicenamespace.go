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

package fake

import (
	"context"
	json "encoding/json"
	"fmt"

	v1alpha1 "github.com/kube-bind/kube-bind/pkg/apis/kubebind/v1alpha1"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"

	kubebindv1alpha1 "github.com/kcp-dev/kcp/contrib/kube-bind/clients/applyconfiguration/kubebind/v1alpha1"
)

// FakeAPIServiceNamespaces implements APIServiceNamespaceInterface
type FakeAPIServiceNamespaces struct {
	Fake *FakeKubeBindV1alpha1
	ns   string
}

var apiservicenamespacesResource = v1alpha1.SchemeGroupVersion.WithResource("apiservicenamespaces")

var apiservicenamespacesKind = v1alpha1.SchemeGroupVersion.WithKind("APIServiceNamespace")

// Get takes name of the aPIServiceNamespace, and returns the corresponding aPIServiceNamespace object, and an error if there is any.
func (c *FakeAPIServiceNamespaces) Get(ctx context.Context, name string, options v1.GetOptions) (result *v1alpha1.APIServiceNamespace, err error) {
	emptyResult := &v1alpha1.APIServiceNamespace{}
	obj, err := c.Fake.
		Invokes(testing.NewGetActionWithOptions(apiservicenamespacesResource, c.ns, name, options), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.APIServiceNamespace), err
}

// List takes label and field selectors, and returns the list of APIServiceNamespaces that match those selectors.
func (c *FakeAPIServiceNamespaces) List(ctx context.Context, opts v1.ListOptions) (result *v1alpha1.APIServiceNamespaceList, err error) {
	emptyResult := &v1alpha1.APIServiceNamespaceList{}
	obj, err := c.Fake.
		Invokes(testing.NewListActionWithOptions(apiservicenamespacesResource, apiservicenamespacesKind, c.ns, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1alpha1.APIServiceNamespaceList{ListMeta: obj.(*v1alpha1.APIServiceNamespaceList).ListMeta}
	for _, item := range obj.(*v1alpha1.APIServiceNamespaceList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested aPIServiceNamespaces.
func (c *FakeAPIServiceNamespaces) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewWatchActionWithOptions(apiservicenamespacesResource, c.ns, opts))

}

// Create takes the representation of a aPIServiceNamespace and creates it.  Returns the server's representation of the aPIServiceNamespace, and an error, if there is any.
func (c *FakeAPIServiceNamespaces) Create(ctx context.Context, aPIServiceNamespace *v1alpha1.APIServiceNamespace, opts v1.CreateOptions) (result *v1alpha1.APIServiceNamespace, err error) {
	emptyResult := &v1alpha1.APIServiceNamespace{}
	obj, err := c.Fake.
		Invokes(testing.NewCreateActionWithOptions(apiservicenamespacesResource, c.ns, aPIServiceNamespace, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.APIServiceNamespace), err
}

// Update takes the representation of a aPIServiceNamespace and updates it. Returns the server's representation of the aPIServiceNamespace, and an error, if there is any.
func (c *FakeAPIServiceNamespaces) Update(ctx context.Context, aPIServiceNamespace *v1alpha1.APIServiceNamespace, opts v1.UpdateOptions) (result *v1alpha1.APIServiceNamespace, err error) {
	emptyResult := &v1alpha1.APIServiceNamespace{}
	obj, err := c.Fake.
		Invokes(testing.NewUpdateActionWithOptions(apiservicenamespacesResource, c.ns, aPIServiceNamespace, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.APIServiceNamespace), err
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *FakeAPIServiceNamespaces) UpdateStatus(ctx context.Context, aPIServiceNamespace *v1alpha1.APIServiceNamespace, opts v1.UpdateOptions) (result *v1alpha1.APIServiceNamespace, err error) {
	emptyResult := &v1alpha1.APIServiceNamespace{}
	obj, err := c.Fake.
		Invokes(testing.NewUpdateSubresourceActionWithOptions(apiservicenamespacesResource, "status", c.ns, aPIServiceNamespace, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.APIServiceNamespace), err
}

// Delete takes name of the aPIServiceNamespace and deletes it. Returns an error if one occurs.
func (c *FakeAPIServiceNamespaces) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewDeleteActionWithOptions(apiservicenamespacesResource, c.ns, name, opts), &v1alpha1.APIServiceNamespace{})

	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakeAPIServiceNamespaces) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	action := testing.NewDeleteCollectionActionWithOptions(apiservicenamespacesResource, c.ns, opts, listOpts)

	_, err := c.Fake.Invokes(action, &v1alpha1.APIServiceNamespaceList{})
	return err
}

// Patch applies the patch and returns the patched aPIServiceNamespace.
func (c *FakeAPIServiceNamespaces) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1alpha1.APIServiceNamespace, err error) {
	emptyResult := &v1alpha1.APIServiceNamespace{}
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceActionWithOptions(apiservicenamespacesResource, c.ns, name, pt, data, opts, subresources...), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.APIServiceNamespace), err
}

// Apply takes the given apply declarative configuration, applies it and returns the applied aPIServiceNamespace.
func (c *FakeAPIServiceNamespaces) Apply(ctx context.Context, aPIServiceNamespace *kubebindv1alpha1.APIServiceNamespaceApplyConfiguration, opts v1.ApplyOptions) (result *v1alpha1.APIServiceNamespace, err error) {
	if aPIServiceNamespace == nil {
		return nil, fmt.Errorf("aPIServiceNamespace provided to Apply must not be nil")
	}
	data, err := json.Marshal(aPIServiceNamespace)
	if err != nil {
		return nil, err
	}
	name := aPIServiceNamespace.Name
	if name == nil {
		return nil, fmt.Errorf("aPIServiceNamespace.Name must be provided to Apply")
	}
	emptyResult := &v1alpha1.APIServiceNamespace{}
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceActionWithOptions(apiservicenamespacesResource, c.ns, *name, types.ApplyPatchType, data, opts.ToPatchOptions()), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.APIServiceNamespace), err
}

// ApplyStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating ApplyStatus().
func (c *FakeAPIServiceNamespaces) ApplyStatus(ctx context.Context, aPIServiceNamespace *kubebindv1alpha1.APIServiceNamespaceApplyConfiguration, opts v1.ApplyOptions) (result *v1alpha1.APIServiceNamespace, err error) {
	if aPIServiceNamespace == nil {
		return nil, fmt.Errorf("aPIServiceNamespace provided to Apply must not be nil")
	}
	data, err := json.Marshal(aPIServiceNamespace)
	if err != nil {
		return nil, err
	}
	name := aPIServiceNamespace.Name
	if name == nil {
		return nil, fmt.Errorf("aPIServiceNamespace.Name must be provided to Apply")
	}
	emptyResult := &v1alpha1.APIServiceNamespace{}
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceActionWithOptions(apiservicenamespacesResource, c.ns, *name, types.ApplyPatchType, data, opts.ToPatchOptions(), "status"), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.APIServiceNamespace), err
}
