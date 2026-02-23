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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	"github.com/kcp-dev/logicalcluster/v3"
)

func NewSimpleDynamicClient(scheme *runtime.Scheme, objects ...runtime.Object) *FakeDynamicClusterClientset {
	unstructuredScheme := runtime.NewScheme()
	for gvk := range scheme.AllKnownTypes() {
		if unstructuredScheme.Recognizes(gvk) {
			continue
		}
		if strings.HasSuffix(gvk.Kind, "List") {
			unstructuredScheme.AddKnownTypeWithName(gvk, &unstructured.UnstructuredList{})
			continue
		}
		unstructuredScheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	}

	objects, err := convertObjectsToUnstructured(scheme, objects)
	if err != nil {
		panic(err)
	}

	for _, obj := range objects {
		gvk := obj.GetObjectKind().GroupVersionKind()
		if !unstructuredScheme.Recognizes(gvk) {
			unstructuredScheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		}
		gvk.Kind += "List"
		if !unstructuredScheme.Recognizes(gvk) {
			unstructuredScheme.AddKnownTypeWithName(gvk, &unstructured.UnstructuredList{})
		}
	}

	return NewSimpleDynamicClientWithCustomListKinds(unstructuredScheme, nil, objects...)
}

// NewSimpleDynamicClientWithCustomListKinds try not to use this.  In general you want to have the scheme have the List types registered
// and allow the default guessing for resources match.  Sometimes that doesn't work, so you can specify a custom mapping here.
func NewSimpleDynamicClientWithCustomListKinds(scheme *runtime.Scheme, gvrToListKind map[schema.GroupVersionResource]string, objects ...runtime.Object) *FakeDynamicClusterClientset {
	// In order to use List with this client, you have to have your lists registered so that the object tracker will find them
	// in the scheme to support the t.scheme.New(listGVK) call when it's building the return value.
	// Since the base fake client needs the listGVK passed through the action (in cases where there are no instances, it
	// cannot look up the actual hits), we need to know a mapping of GVR to listGVK here.  For GETs and other types of calls,
	// there is no return value that contains a GVK, so it doesn't have to know the mapping in advance.

	// first we attempt to invert known List types from the scheme to auto guess the resource with unsafe guesses
	// this covers common usage of registering types in scheme and passing them
	completeGVRToListKind := map[schema.GroupVersionResource]string{}
	for listGVK := range scheme.AllKnownTypes() {
		if !strings.HasSuffix(listGVK.Kind, "List") {
			continue
		}
		nonListGVK := listGVK.GroupVersion().WithKind(listGVK.Kind[:len(listGVK.Kind)-4])
		plural, _ := meta.UnsafeGuessKindToResource(nonListGVK)
		completeGVRToListKind[plural] = listGVK.Kind
	}

	for gvr, listKind := range gvrToListKind {
		if !strings.HasSuffix(listKind, "List") {
			panic("coding error, listGVK must end in List or this fake client doesn't work right")
		}
		listGVK := gvr.GroupVersion().WithKind(listKind)

		// if we already have this type registered, just skip it
		if _, err := scheme.New(listGVK); err == nil {
			completeGVRToListKind[gvr] = listKind
			continue
		}

		scheme.AddKnownTypeWithName(listGVK, &unstructured.UnstructuredList{})
		completeGVRToListKind[gvr] = listKind
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

	cs := &FakeDynamicClusterClientset{Fake: &kcptesting.Fake{}, tracker: o, scheme: scheme, gvrToListKind: completeGVRToListKind}
	cs.AddReactor("*", "*", kcptesting.ObjectReaction(o))
	cs.AddWatchReactor("*", kcptesting.WatchReaction(o))

	return cs
}

var (
	_ kcpdynamic.ClusterInterface = &FakeDynamicClusterClientset{}
	_ kcptesting.FakeClient       = &FakeDynamicClusterClientset{}
)

// Clientset implements clientset.Interface. Meant to be embedded into a
// struct to get a default implementation. This makes faking out just the method
// you want to test easier.
type FakeDynamicClusterClientset struct {
	*kcptesting.Fake
	scheme        *runtime.Scheme
	gvrToListKind map[schema.GroupVersionResource]string
	tracker       kcptesting.ObjectTracker
}

func (c *FakeDynamicClusterClientset) Tracker() kcptesting.ObjectTracker {
	return c.tracker
}

func (c *FakeDynamicClusterClientset) Cluster(clusterPath logicalcluster.Path) dynamic.Interface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return &FakeDynamicClient{
		Fake:          c.Fake,
		tracker:       c.tracker.Cluster(clusterPath),
		clusterPath:   clusterPath,
		gvrToListKind: c.gvrToListKind,
	}
}

func (c *FakeDynamicClusterClientset) Resource(resource schema.GroupVersionResource) kcpdynamic.ResourceClusterInterface {
	return &FakeDynamicClusterClient{
		Fake:          c.Fake,
		scheme:        c.scheme,
		gvrToListKind: c.gvrToListKind,
		tracker:       c.tracker,
		resource:      resource,
	}
}

var (
	_ kcpdynamic.ResourceClusterInterface = &FakeDynamicClusterClient{}
	_ kcptesting.FakeClient               = &FakeDynamicClusterClient{}
)

type FakeDynamicClusterClient struct {
	*kcptesting.Fake
	scheme        *runtime.Scheme
	gvrToListKind map[schema.GroupVersionResource]string
	tracker       kcptesting.ObjectTracker
	resource      schema.GroupVersionResource
}

func (f *FakeDynamicClusterClient) Tracker() kcptesting.ObjectTracker {
	return f.tracker
}

func (f *FakeDynamicClusterClient) Cluster(clusterPath logicalcluster.Path) dynamic.NamespaceableResourceInterface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return f.cluster(clusterPath)
}

func (f *FakeDynamicClusterClient) cluster(clusterPath logicalcluster.Path) dynamic.NamespaceableResourceInterface {
	return &dynamicResourceClient{
		client: &FakeDynamicClient{
			Fake:          f.Fake,
			tracker:       f.tracker.Cluster(clusterPath),
			clusterPath:   clusterPath,
			gvrToListKind: f.gvrToListKind,
		},
		resource: f.resource,
		listKind: f.gvrToListKind[f.resource],
	}
}

func (f *FakeDynamicClusterClient) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	return f.cluster(logicalcluster.Wildcard).List(ctx, opts)
}

func (f *FakeDynamicClusterClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	return f.cluster(logicalcluster.Wildcard).Watch(ctx, opts)
}

var (
	_ dynamic.Interface           = &FakeDynamicClient{}
	_ kcptesting.FakeScopedClient = &FakeDynamicClient{}
)

type FakeDynamicClient struct {
	*kcptesting.Fake
	scheme        *runtime.Scheme
	gvrToListKind map[schema.GroupVersionResource]string
	tracker       kcptesting.ScopedObjectTracker
	clusterPath   logicalcluster.Path
}

func (f *FakeDynamicClient) Tracker() kcptesting.ScopedObjectTracker {
	return f.tracker
}

func (f *FakeDynamicClient) Resource(resource schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return &dynamicResourceClient{client: f, resource: resource, listKind: f.gvrToListKind[resource]}
}

func (c *FakeDynamicClient) IsWatchListSemanticsUnSupported() bool {
	return true
}

type dynamicResourceClient struct {
	client    *FakeDynamicClient
	namespace string
	resource  schema.GroupVersionResource
	listKind  string
}

func (c *dynamicResourceClient) Namespace(ns string) dynamic.ResourceInterface {
	ret := *c
	ret.namespace = ns
	return &ret
}

func (c *dynamicResourceClient) Create(ctx context.Context, obj *unstructured.Unstructured, opts metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var uncastRet runtime.Object
	var err error
	switch {
	case len(c.namespace) == 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootCreateActionWithOptions(c.resource, c.client.clusterPath, obj, opts), obj)

	case len(c.namespace) == 0 && len(subresources) > 0:
		var accessor metav1.Object // avoid shadowing err
		accessor, err = meta.Accessor(obj)
		if err != nil {
			return nil, err
		}
		name := accessor.GetName()
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootCreateSubresourceActionWithOptions(c.resource, c.client.clusterPath, name, strings.Join(subresources, "/"), obj, opts), obj)

	case len(c.namespace) > 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewCreateActionWithOptions(c.resource, c.client.clusterPath, c.namespace, obj, opts), obj)

	case len(c.namespace) > 0 && len(subresources) > 0:
		var accessor metav1.Object // avoid shadowing err
		accessor, err = meta.Accessor(obj)
		if err != nil {
			return nil, err
		}
		name := accessor.GetName()
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewCreateSubresourceActionWithOptions(c.resource, c.client.clusterPath, name, strings.Join(subresources, "/"), c.namespace, obj, opts), obj)

	}

	if err != nil {
		return nil, err
	}
	if uncastRet == nil {
		return nil, err
	}

	ret := &unstructured.Unstructured{}
	if err := c.client.scheme.Convert(uncastRet, ret, nil); err != nil {
		return nil, err
	}
	return ret, err
}

func (c *dynamicResourceClient) Update(ctx context.Context, obj *unstructured.Unstructured, opts metav1.UpdateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var uncastRet runtime.Object
	var err error
	switch {
	case len(c.namespace) == 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootUpdateActionWithOptions(c.resource, c.client.clusterPath, obj, opts), obj)

	case len(c.namespace) == 0 && len(subresources) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootUpdateSubresourceActionWithOptions(c.resource, c.client.clusterPath, strings.Join(subresources, "/"), obj, opts), obj)

	case len(c.namespace) > 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewUpdateActionWithOptions(c.resource, c.client.clusterPath, c.namespace, obj, opts), obj)

	case len(c.namespace) > 0 && len(subresources) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewUpdateSubresourceActionWithOptions(c.resource, c.client.clusterPath, strings.Join(subresources, "/"), c.namespace, obj, opts), obj)

	}

	if err != nil {
		return nil, err
	}
	if uncastRet == nil {
		return nil, err
	}

	ret := &unstructured.Unstructured{}
	if err := c.client.scheme.Convert(uncastRet, ret, nil); err != nil {
		return nil, err
	}
	return ret, err
}

func (c *dynamicResourceClient) UpdateStatus(ctx context.Context, obj *unstructured.Unstructured, opts metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	var uncastRet runtime.Object
	var err error
	switch {
	case len(c.namespace) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootUpdateSubresourceActionWithOptions(c.resource, c.client.clusterPath, "status", obj, opts), obj)

	case len(c.namespace) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewUpdateSubresourceActionWithOptions(c.resource, c.client.clusterPath, "status", c.namespace, obj, opts), obj)

	}

	if err != nil {
		return nil, err
	}
	if uncastRet == nil {
		return nil, err
	}

	ret := &unstructured.Unstructured{}
	if err := c.client.scheme.Convert(uncastRet, ret, nil); err != nil {
		return nil, err
	}
	return ret, err
}

func (c *dynamicResourceClient) Delete(ctx context.Context, name string, opts metav1.DeleteOptions, subresources ...string) error {
	var err error
	switch {
	case len(c.namespace) == 0 && len(subresources) == 0:
		_, err = c.client.Fake.
			Invokes(kcptesting.NewRootDeleteActionWithOptions(c.resource, c.client.clusterPath, name, opts), &metav1.Status{Status: "dynamic delete fail"})

	case len(c.namespace) == 0 && len(subresources) > 0:
		_, err = c.client.Fake.
			Invokes(kcptesting.NewRootDeleteSubresourceActionWithOptions(c.resource, c.client.clusterPath, strings.Join(subresources, "/"), name, opts), &metav1.Status{Status: "dynamic delete fail"})

	case len(c.namespace) > 0 && len(subresources) == 0:
		_, err = c.client.Fake.
			Invokes(kcptesting.NewDeleteActionWithOptions(c.resource, c.client.clusterPath, c.namespace, name, opts), &metav1.Status{Status: "dynamic delete fail"})

	case len(c.namespace) > 0 && len(subresources) > 0:
		_, err = c.client.Fake.
			Invokes(kcptesting.NewDeleteSubresourceActionWithOptions(c.resource, c.client.clusterPath, strings.Join(subresources, "/"), c.namespace, name, opts), &metav1.Status{Status: "dynamic delete fail"})
	}

	return err
}

func (c *dynamicResourceClient) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOptions metav1.ListOptions) error {
	var err error
	switch {
	case len(c.namespace) == 0:
		action := kcptesting.NewRootDeleteCollectionActionWithOptions(c.resource, c.client.clusterPath, opts, listOptions)
		_, err = c.client.Fake.Invokes(action, &metav1.Status{Status: "dynamic deletecollection fail"})

	case len(c.namespace) > 0:
		action := kcptesting.NewDeleteCollectionActionWithOptions(c.resource, c.client.clusterPath, c.namespace, opts, listOptions)
		_, err = c.client.Fake.Invokes(action, &metav1.Status{Status: "dynamic deletecollection fail"})

	}

	return err
}

func (c *dynamicResourceClient) Get(ctx context.Context, name string, opts metav1.GetOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var uncastRet runtime.Object
	var err error
	switch {
	case len(c.namespace) == 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootGetActionWithOptions(c.resource, c.client.clusterPath, name, opts), &metav1.Status{Status: "dynamic get fail"})

	case len(c.namespace) == 0 && len(subresources) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootGetSubresourceActionWithOptions(c.resource, c.client.clusterPath, strings.Join(subresources, "/"), name, opts), &metav1.Status{Status: "dynamic get fail"})

	case len(c.namespace) > 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewGetActionWithOptions(c.resource, c.client.clusterPath, c.namespace, name, opts), &metav1.Status{Status: "dynamic get fail"})

	case len(c.namespace) > 0 && len(subresources) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewGetSubresourceActionWithOptions(c.resource, c.client.clusterPath, c.namespace, strings.Join(subresources, "/"), name, opts), &metav1.Status{Status: "dynamic get fail"})
	}

	if err != nil {
		return nil, err
	}
	if uncastRet == nil {
		return nil, err
	}

	ret := &unstructured.Unstructured{}
	if err := c.client.scheme.Convert(uncastRet, ret, nil); err != nil {
		return nil, err
	}
	return ret, err
}

func (c *dynamicResourceClient) List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	if len(c.listKind) == 0 {
		panic(fmt.Sprintf("coding error: you must register resource to list kind for every resource you're going to LIST when creating the client.  See NewSimpleDynamicClientWithCustomListKinds or register the list into the scheme: %v out of %v", c.resource, c.client.gvrToListKind))
	}
	listGVK := c.resource.GroupVersion().WithKind(c.listKind)
	listForFakeClientGVK := c.resource.GroupVersion().WithKind(c.listKind[:len(c.listKind)-4]) /*base library appends List*/

	var obj runtime.Object
	var err error
	switch {
	case len(c.namespace) == 0:
		obj, err = c.client.Fake.
			Invokes(kcptesting.NewRootListActionWithOptions(c.resource, listForFakeClientGVK, c.client.clusterPath, opts), &metav1.Status{Status: "dynamic list fail"})

	case len(c.namespace) > 0:
		obj, err = c.client.Fake.
			Invokes(kcptesting.NewListActionWithOptions(c.resource, c.client.clusterPath, listForFakeClientGVK, c.namespace, opts), &metav1.Status{Status: "dynamic list fail"})

	}

	if obj == nil {
		return nil, err
	}

	label, _, _ := kcptesting.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}

	retUnstructured := &unstructured.Unstructured{}
	if err := c.client.scheme.Convert(obj, retUnstructured, nil); err != nil {
		return nil, err
	}
	entireList, err := retUnstructured.ToList()
	if err != nil {
		return nil, err
	}

	list := &unstructured.UnstructuredList{}
	list.SetRemainingItemCount(entireList.GetRemainingItemCount())
	list.SetResourceVersion(entireList.GetResourceVersion())
	list.SetContinue(entireList.GetContinue())
	list.GetObjectKind().SetGroupVersionKind(listGVK)
	for i := range entireList.Items {
		item := &entireList.Items[i]
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

func (c *dynamicResourceClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	switch {
	case len(c.namespace) == 0:
		return c.client.Fake.
			InvokesWatch(kcptesting.NewRootWatchActionWithOptions(c.resource, c.client.clusterPath, opts))

	case len(c.namespace) > 0:
		return c.client.Fake.
			InvokesWatch(kcptesting.NewWatchActionWithOptions(c.resource, c.client.clusterPath, c.namespace, opts))

	}

	panic("math broke")
}

// TODO: opts are currently ignored.
func (c *dynamicResourceClient) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*unstructured.Unstructured, error) {
	var uncastRet runtime.Object
	var err error
	switch {
	case len(c.namespace) == 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootPatchActionWithOptions(c.resource, c.client.clusterPath, name, pt, data, opts), &metav1.Status{Status: "dynamic patch fail"})

	case len(c.namespace) == 0 && len(subresources) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootPatchSubresourceActionWithOptions(c.resource, c.client.clusterPath, name, pt, data, opts, subresources...), &metav1.Status{Status: "dynamic patch fail"})

	case len(c.namespace) > 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewPatchActionWithOptions(c.resource, c.client.clusterPath, c.namespace, name, pt, data, opts), &metav1.Status{Status: "dynamic patch fail"})

	case len(c.namespace) > 0 && len(subresources) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewPatchSubresourceActionWithOptions(c.resource, c.client.clusterPath, c.namespace, name, pt, data, opts, subresources...), &metav1.Status{Status: "dynamic patch fail"})

	}

	if err != nil {
		return nil, err
	}
	if uncastRet == nil {
		return nil, err
	}

	ret := &unstructured.Unstructured{}
	if err := c.client.scheme.Convert(uncastRet, ret, nil); err != nil {
		return nil, err
	}
	return ret, err
}

// TODO: opts are currently ignored.
func (c *dynamicResourceClient) Apply(ctx context.Context, name string, obj *unstructured.Unstructured, options metav1.ApplyOptions, subresources ...string) (*unstructured.Unstructured, error) {
	outBytes, err := runtime.Encode(unstructured.UnstructuredJSONScheme, obj)
	if err != nil {
		return nil, err
	}
	patchOptions := metav1.PatchOptions{
		Force:        &options.Force,
		DryRun:       options.DryRun,
		FieldManager: options.FieldManager,
	}
	var uncastRet runtime.Object
	switch {
	case len(c.namespace) == 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootPatchActionWithOptions(c.resource, c.client.clusterPath, name, types.ApplyPatchType, outBytes, patchOptions), &metav1.Status{Status: "dynamic patch fail"})

	case len(c.namespace) == 0 && len(subresources) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewRootPatchSubresourceActionWithOptions(c.resource, c.client.clusterPath, name, types.ApplyPatchType, outBytes, patchOptions, subresources...), &metav1.Status{Status: "dynamic patch fail"})

	case len(c.namespace) > 0 && len(subresources) == 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewPatchActionWithOptions(c.resource, c.client.clusterPath, c.namespace, name, types.ApplyPatchType, outBytes, patchOptions), &metav1.Status{Status: "dynamic patch fail"})

	case len(c.namespace) > 0 && len(subresources) > 0:
		uncastRet, err = c.client.Fake.
			Invokes(kcptesting.NewPatchSubresourceActionWithOptions(c.resource, c.client.clusterPath, c.namespace, name, types.ApplyPatchType, outBytes, patchOptions, subresources...), &metav1.Status{Status: "dynamic patch fail"})

	}

	if err != nil {
		return nil, err
	}
	if uncastRet == nil {
		return nil, err
	}

	ret := &unstructured.Unstructured{}
	if err := c.client.scheme.Convert(uncastRet, ret, nil); err != nil {
		return nil, err
	}
	return ret, nil
}

func (c *dynamicResourceClient) ApplyStatus(ctx context.Context, name string, obj *unstructured.Unstructured, options metav1.ApplyOptions) (*unstructured.Unstructured, error) {
	return c.Apply(ctx, name, obj, options, "status")
}

func convertObjectsToUnstructured(s *runtime.Scheme, objs []runtime.Object) ([]runtime.Object, error) {
	ul := make([]runtime.Object, 0, len(objs))

	for _, obj := range objs {
		u, err := convertToUnstructured(s, obj)
		if err != nil {
			return nil, err
		}

		ul = append(ul, u)
	}
	return ul, nil
}

func convertToUnstructured(s *runtime.Scheme, obj runtime.Object) (runtime.Object, error) {
	var (
		err error
		u   unstructured.Unstructured
	)

	u.Object, err = runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to unstructured: %w", err)
	}

	gvk := u.GroupVersionKind()
	if gvk.Group == "" || gvk.Kind == "" {
		gvks, _, err := s.ObjectKinds(obj)
		if err != nil {
			return nil, fmt.Errorf("failed to convert to unstructured - unable to get GVK %w", err)
		}
		apiv, k := gvks[0].ToAPIVersionAndKind()
		u.SetAPIVersion(apiv)
		u.SetKind(k)
	}
	return &u, nil
}
