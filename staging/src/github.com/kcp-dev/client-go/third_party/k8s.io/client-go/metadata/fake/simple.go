/*
Copyright 2018 The Kubernetes Authors.
Modifications Copyright 2022 The KCP Authors.

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

package fake

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/metadata"

	kcpmetadata "github.com/kcp-dev/client-go/metadata"
	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	"github.com/kcp-dev/logicalcluster/v3"
)

// MetadataClient assists in creating fake objects for use when testing, since metadata.Getter
// does not expose create
type MetadataClient interface {
	metadata.Getter
	CreateFake(obj *metav1.PartialObjectMetadata, opts metav1.CreateOptions, subresources ...string) (*metav1.PartialObjectMetadata, error)
	UpdateFake(obj *metav1.PartialObjectMetadata, opts metav1.UpdateOptions, subresources ...string) (*metav1.PartialObjectMetadata, error)
}

// NewTestScheme creates a unique Scheme for each test.
func NewTestScheme() *runtime.Scheme {
	return runtime.NewScheme()
}

// NewSimpleMetadataClient creates a new client that will use the provided scheme and respond with the
// provided objects when requests are made. It will track actions made to the client which can be checked
// with GetActions().
func NewSimpleMetadataClient(scheme *runtime.Scheme, objects ...runtime.Object) *FakeMetadataClusterClientset {
	gvkFakeList := schema.GroupVersionKind{Group: "fake-metadata-client-group", Version: "v1", Kind: "List"}
	if !scheme.Recognizes(gvkFakeList) {
		// In order to use List with this client, you have to have the v1.List registered in your scheme, since this is a test
		// type we modify the input scheme
		scheme.AddKnownTypeWithName(gvkFakeList, &metav1.List{})
	}

	codecs := serializer.NewCodecFactory(scheme)
	o := kcptesting.NewObjectTracker(scheme, codecs.UniversalDecoder())
	for _, obj := range objects {
		metaObj, ok := obj.(logicalcluster.Object)
		if !ok {
			panic(fmt.Sprintf("cannot extract logical cluster from %T", obj))
		}
		if err := o.Cluster(logicalcluster.From(metaObj).Path()).Add(obj); err != nil {
			panic(err)
		}
	}

	cs := &FakeMetadataClusterClientset{Fake: &kcptesting.Fake{}, tracker: o, scheme: scheme}
	cs.AddReactor("*", "*", kcptesting.ObjectReaction(o))
	cs.AddWatchReactor("*", kcptesting.WatchReaction(o))

	return cs
}

var _ kcpmetadata.ClusterInterface = (*FakeMetadataClusterClientset)(nil)
var _ kcptesting.FakeClient = (*FakeMetadataClusterClientset)(nil)

// FakeMetadataClusterClientset implements clientset.Interface. Meant to be embedded into a
// struct to get a default implementation. This makes faking out just the method
// you want to test easier.
type FakeMetadataClusterClientset struct {
	*kcptesting.Fake
	scheme  *runtime.Scheme
	tracker kcptesting.ObjectTracker
}

func (c *FakeMetadataClusterClientset) Tracker() kcptesting.ObjectTracker {
	return c.tracker
}

func (c *FakeMetadataClusterClientset) Cluster(clusterPath logicalcluster.Path) metadata.Interface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return c.cluster(clusterPath)
}

func (c *FakeMetadataClusterClientset) cluster(clusterPath logicalcluster.Path) metadata.Interface {
	return &FakeMetadataClient{
		Fake:        c.Fake,
		tracker:     c.tracker.Cluster(clusterPath),
		clusterPath: clusterPath,
	}
}

func (c *FakeMetadataClusterClientset) Resource(resource schema.GroupVersionResource) kcpmetadata.ResourceClusterInterface {
	return &FakeMetadataClusterClient{
		Fake:     c.Fake,
		scheme:   c.scheme,
		tracker:  c.tracker,
		resource: resource,
	}
}

type FakeMetadataClusterClient struct {
	*kcptesting.Fake
	scheme   *runtime.Scheme
	tracker  kcptesting.ObjectTracker
	resource schema.GroupVersionResource
}

func (f *FakeMetadataClusterClient) Cluster(clusterPath logicalcluster.Path) metadata.Getter {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return f.cluster(clusterPath)
}

func (f *FakeMetadataClusterClient) cluster(clusterPath logicalcluster.Path) metadata.Getter {
	return &metadataResourceClient{
		client: &FakeMetadataClient{
			Fake:        f.Fake,
			scheme:      f.scheme,
			tracker:     f.tracker.Cluster(clusterPath),
			clusterPath: clusterPath,
		},
		resource: f.resource,
	}
}

func (f *FakeMetadataClusterClient) List(ctx context.Context, opts metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	return f.cluster(logicalcluster.Wildcard).List(ctx, opts)
}

func (f *FakeMetadataClusterClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return f.cluster(logicalcluster.Wildcard).Watch(ctx, opts)
}

type FakeMetadataClient struct {
	*kcptesting.Fake
	scheme      *runtime.Scheme
	tracker     kcptesting.ScopedObjectTracker
	clusterPath logicalcluster.Path
}

var (
	_ metadata.Interface          = &FakeMetadataClient{}
	_ kcptesting.FakeScopedClient = &FakeMetadataClient{}
)

func (c *FakeMetadataClient) Tracker() kcptesting.ScopedObjectTracker {
	return c.tracker
}

// Resource returns an interface for accessing the provided resource.
func (c *FakeMetadataClient) Resource(resource schema.GroupVersionResource) metadata.Getter {
	return &metadataResourceClient{client: c, resource: resource}
}

func (c *FakeMetadataClient) IsWatchListSemanticsUnSupported() bool {
	return true
}

type metadataResourceClient struct {
	client    *FakeMetadataClient
	namespace string
	resource  schema.GroupVersionResource
}

// Namespace returns an interface for accessing the current resource in the specified
// namespace.
func (c *metadataResourceClient) Namespace(ns string) metadata.ResourceInterface {
	ret := *c
	ret.namespace = ns
	return &ret
}

// CreateFake records the object creation and processes it via the reactor.
func (c *metadataResourceClient) CreateFake(obj *metav1.PartialObjectMetadata, opts metav1.CreateOptions, subresources ...string) (*metav1.PartialObjectMetadata, error) {
	var uncastRet runtime.Object
	var err error
	switch {
	case len(c.namespace) == 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootCreateAction(c.resource, c.client.clusterPath, obj), obj)

	case len(c.namespace) == 0 && len(subresources) > 0:
		var accessor metav1.Object // avoid shadowing err
		accessor, err = meta.Accessor(obj)
		if err != nil {
			return nil, err
		}
		name := accessor.GetName()
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootCreateSubresourceAction(c.resource, c.client.clusterPath, name, strings.Join(subresources, "/"), obj), obj)

	case len(c.namespace) > 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewCreateAction(c.resource, c.client.clusterPath, c.namespace, obj), obj)

	case len(c.namespace) > 0 && len(subresources) > 0:
		var accessor metav1.Object // avoid shadowing err
		accessor, err = meta.Accessor(obj)
		if err != nil {
			return nil, err
		}
		name := accessor.GetName()
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewCreateSubresourceAction(c.resource, c.client.clusterPath, name, strings.Join(subresources, "/"), c.namespace, obj), obj)

	}

	if err != nil {
		return nil, err
	}
	if uncastRet == nil {
		return nil, err
	}
	ret, ok := uncastRet.(*metav1.PartialObjectMetadata)
	if !ok {
		return nil, fmt.Errorf("unexpected return value type %T", uncastRet)
	}
	return ret, err
}

// UpdateFake records the object update and processes it via the reactor.
func (c *metadataResourceClient) UpdateFake(obj *metav1.PartialObjectMetadata, opts metav1.UpdateOptions, subresources ...string) (*metav1.PartialObjectMetadata, error) {
	var uncastRet runtime.Object
	var err error
	switch {
	case len(c.namespace) == 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootUpdateAction(c.resource, c.client.clusterPath, obj), obj)

	case len(c.namespace) == 0 && len(subresources) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootUpdateSubresourceAction(c.resource, c.client.clusterPath, strings.Join(subresources, "/"), obj), obj)

	case len(c.namespace) > 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewUpdateAction(c.resource, c.client.clusterPath, c.namespace, obj), obj)

	case len(c.namespace) > 0 && len(subresources) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewUpdateSubresourceAction(c.resource, c.client.clusterPath, strings.Join(subresources, "/"), c.namespace, obj), obj)

	}

	if err != nil {
		return nil, err
	}
	if uncastRet == nil {
		return nil, err
	}
	ret, ok := uncastRet.(*metav1.PartialObjectMetadata)
	if !ok {
		return nil, fmt.Errorf("unexpected return value type %T", uncastRet)
	}
	return ret, err
}

// UpdateStatus records the object status update and processes it via the reactor.
func (c *metadataResourceClient) UpdateStatus(obj *metav1.PartialObjectMetadata, opts metav1.UpdateOptions) (*metav1.PartialObjectMetadata, error) {
	var uncastRet runtime.Object
	var err error
	switch {
	case len(c.namespace) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootUpdateSubresourceAction(c.resource, c.client.clusterPath, "status", obj), obj)

	case len(c.namespace) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewUpdateSubresourceAction(c.resource, c.client.clusterPath, "status", c.namespace, obj), obj)

	}

	if err != nil {
		return nil, err
	}
	if uncastRet == nil {
		return nil, err
	}
	ret, ok := uncastRet.(*metav1.PartialObjectMetadata)
	if !ok {
		return nil, fmt.Errorf("unexpected return value type %T", uncastRet)
	}
	return ret, err
}

// Delete records the object deletion and processes it via the reactor.
func (c *metadataResourceClient) Delete(ctx context.Context, name string, opts metav1.DeleteOptions, subresources ...string) error {
	var err error
	switch {
	case len(c.namespace) == 0 && len(subresources) == 0:
		_, err = c.client.Fake.
			Invokes(kcptesting.NewRootDeleteAction(c.resource, c.client.clusterPath, name), &metav1.Status{Status: "metadata delete fail"})

	case len(c.namespace) == 0 && len(subresources) > 0:
		_, err = c.client.Fake.
			Invokes(kcptesting.NewRootDeleteSubresourceAction(c.resource, c.client.clusterPath, strings.Join(subresources, "/"), name), &metav1.Status{Status: "metadata delete fail"})

	case len(c.namespace) > 0 && len(subresources) == 0:
		_, err = c.client.Fake.
			Invokes(kcptesting.NewDeleteAction(c.resource, c.client.clusterPath, c.namespace, name), &metav1.Status{Status: "metadata delete fail"})

	case len(c.namespace) > 0 && len(subresources) > 0:
		_, err = c.client.Fake.
			Invokes(kcptesting.NewDeleteSubresourceAction(c.resource, c.client.clusterPath, strings.Join(subresources, "/"), c.namespace, name), &metav1.Status{Status: "metadata delete fail"})
	}

	return err
}

// DeleteCollection records the object collection deletion and processes it via the reactor.
func (c *metadataResourceClient) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOptions metav1.ListOptions) error {
	var err error
	switch {
	case len(c.namespace) == 0:
		action := kcptesting.NewRootDeleteCollectionAction(c.resource, c.client.clusterPath, listOptions)
		_, err = c.client.Fake.Invokes(action, &metav1.Status{Status: "metadata deletecollection fail"})

	case len(c.namespace) > 0:
		action := kcptesting.NewDeleteCollectionAction(c.resource, c.client.clusterPath, c.namespace, listOptions)
		_, err = c.client.Fake.Invokes(action, &metav1.Status{Status: "metadata deletecollection fail"})

	}

	return err
}

// Get records the object retrieval and processes it via the reactor.
func (c *metadataResourceClient) Get(ctx context.Context, name string, opts metav1.GetOptions, subresources ...string) (*metav1.PartialObjectMetadata, error) {
	var uncastRet runtime.Object
	var err error
	switch {
	case len(c.namespace) == 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootGetAction(c.resource, c.client.clusterPath, name), &metav1.Status{Status: "metadata get fail"})

	case len(c.namespace) == 0 && len(subresources) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootGetSubresourceAction(c.resource, c.client.clusterPath, strings.Join(subresources, "/"), name), &metav1.Status{Status: "metadata get fail"})

	case len(c.namespace) > 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewGetAction(c.resource, c.client.clusterPath, c.namespace, name), &metav1.Status{Status: "metadata get fail"})

	case len(c.namespace) > 0 && len(subresources) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewGetSubresourceAction(c.resource, c.client.clusterPath, c.namespace, strings.Join(subresources, "/"), name), &metav1.Status{Status: "metadata get fail"})
	}

	if err != nil {
		return nil, err
	}
	if uncastRet == nil {
		return nil, err
	}
	ret, ok := uncastRet.(*metav1.PartialObjectMetadata)
	if !ok {
		return nil, fmt.Errorf("unexpected return value type %T", uncastRet)
	}
	return ret, err
}

// List records the object deletion and processes it via the reactor.
func (c *metadataResourceClient) List(ctx context.Context, opts metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	var obj runtime.Object
	var err error
	switch {
	case len(c.namespace) == 0:
		obj, err = c.client.Fake.
			Invokes(kcptesting.NewRootListAction(c.resource, schema.GroupVersionKind{Group: "fake-metadata-client-group", Version: "v1", Kind: "" /*List is appended by the tracker automatically*/}, c.client.clusterPath, opts), &metav1.Status{Status: "metadata list fail"})

	case len(c.namespace) > 0:
		obj, err = c.client.Fake.
			Invokes(kcptesting.NewListAction(c.resource, schema.GroupVersionKind{Group: "fake-metadata-client-group", Version: "v1", Kind: "" /*List is appended by the tracker automatically*/}, c.client.clusterPath, c.namespace, opts), &metav1.Status{Status: "metadata list fail"})

	}

	if obj == nil {
		return nil, err
	}

	label, _, _ := kcptesting.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}

	inputList, ok := obj.(*metav1.List)
	if !ok {
		return nil, fmt.Errorf("incoming object is incorrect type %T", obj)
	}

	list := &metav1.PartialObjectMetadataList{
		ListMeta: inputList.ListMeta,
	}
	for i := range inputList.Items {
		item, ok := inputList.Items[i].Object.(*metav1.PartialObjectMetadata)
		if !ok {
			return nil, fmt.Errorf("item %d in list %T is %T", i, inputList, inputList.Items[i].Object)
		}
		metadata, err := meta.Accessor(item)
		if err != nil {
			return nil, err
		}
		if label.Matches(labels.Set(metadata.GetLabels())) {
			list.Items = append(list.Items, *item)
		}
	}
	return list, nil
}

func (c *metadataResourceClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	switch {
	case len(c.namespace) == 0:
		return c.client.Fake.
			InvokesWatch(kcptesting.NewRootWatchAction(c.resource, c.client.clusterPath, opts))

	case len(c.namespace) > 0:
		return c.client.Fake.
			InvokesWatch(kcptesting.NewWatchAction(c.resource, c.client.clusterPath, c.namespace, opts))

	}

	panic("math broke")
}

// Patch records the object patch and processes it via the reactor.
func (c *metadataResourceClient) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*metav1.PartialObjectMetadata, error) {
	var uncastRet runtime.Object
	var err error
	switch {
	case len(c.namespace) == 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootPatchAction(c.resource, c.client.clusterPath, name, pt, data), &metav1.Status{Status: "metadata patch fail"})

	case len(c.namespace) == 0 && len(subresources) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootPatchSubresourceAction(c.resource, c.client.clusterPath, name, pt, data, subresources...), &metav1.Status{Status: "metadata patch fail"})

	case len(c.namespace) > 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewPatchAction(c.resource, c.client.clusterPath, c.namespace, name, pt, data), &metav1.Status{Status: "metadata patch fail"})

	case len(c.namespace) > 0 && len(subresources) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewPatchSubresourceAction(c.resource, c.client.clusterPath, c.namespace, name, pt, data, subresources...), &metav1.Status{Status: "metadata patch fail"})

	}

	if err != nil {
		return nil, err
	}
	if uncastRet == nil {
		return nil, err
	}
	ret, ok := uncastRet.(*metav1.PartialObjectMetadata)
	if !ok {
		return nil, fmt.Errorf("unexpected return value type %T", uncastRet)
	}
	return ret, err
}
