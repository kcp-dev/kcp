/*
Copyright 2024 The Kubernetes Authors.

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

package gentype

import (
	"context"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	watch "k8s.io/apimachinery/pkg/watch"

	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
)

// FakeClusterClient represents a fake cluster client
type FakeClusterClient[T objectWithMeta] struct {
	*kcptesting.Fake
	resource  schema.GroupVersionResource
	kind      schema.GroupVersionKind
	newObject func() T
}

// FakeClusterClientWithList represents a fake cluster client with support for lists.
type FakeClusterClientWithList[T objectWithMeta, L runtime.Object] struct {
	*FakeClusterClient[T]
	alsoFakeClusterLister[T, L]
}

// Helper types for composition
type alsoFakeClusterLister[T objectWithMeta, L runtime.Object] struct {
	client       *FakeClusterClient[T]
	newList      func() L
	copyListMeta func(L, L)
	getItems     func(L) []T
	setItems     func(L, []T)
}

// NewFakeClient constructs a fake client, namespaced or not, with no support for lists or apply.
// Non-namespaced clients are constructed by passing an empty namespace ("").
func NewFakeClusterClient[T objectWithMeta](
	fake *kcptesting.Fake, resource schema.GroupVersionResource, kind schema.GroupVersionKind, emptyObjectCreator func() T,
) *FakeClusterClient[T] {
	return &FakeClusterClient[T]{fake, resource, kind, emptyObjectCreator}
}

// NewFakeClusterClientWithList constructs a namespaced client with support for lists.
func NewFakeClusterClientWithList[T objectWithMeta, L runtime.Object](
	fake *kcptesting.Fake, resource schema.GroupVersionResource, kind schema.GroupVersionKind, emptyObjectCreator func() T,
	emptyListCreator func() L, listMetaCopier func(L, L), itemGetter func(L) []T, itemSetter func(L, []T),
) *FakeClusterClientWithList[T, L] {
	fakeClusterClient := NewFakeClusterClient[T](fake, resource, kind, emptyObjectCreator)
	return &FakeClusterClientWithList[T, L]{
		fakeClusterClient,
		alsoFakeClusterLister[T, L]{fakeClusterClient, emptyListCreator, listMetaCopier, itemGetter, itemSetter},
	}
}

// List takes label and field selectors, and returns the list of resources that match those selectors.
func (l *alsoFakeClusterLister[T, L]) List(ctx context.Context, opts metav1.ListOptions) (result L, err error) {
	emptyResult := l.newList()
	obj, err := l.client.Fake.
		Invokes(kcptesting.NewListAction(l.client.resource, l.client.kind, logicalcluster.Wildcard, metav1.NamespaceAll, opts), emptyResult)
	if obj == nil {
		return emptyResult, err
	}

	label, _, _ := kcptesting.ExtractFromListOptions(opts)
	if label == nil {
		// Everything matches
		return obj.(L), nil
	}
	list := l.newList()
	l.copyListMeta(list, obj.(L))
	var items []T
	for _, item := range l.getItems(obj.(L)) {
		itemMeta, err := meta.Accessor(item)
		if err != nil {
			// No ObjectMeta, nothing can match
			continue
		}
		if label.Matches(labels.Set(itemMeta.GetLabels())) {
			items = append(items, item)
		}
	}
	l.setItems(list, items)
	return list, err
}

// Watch returns a watch.Interface that watches the requested resources.
func (c *FakeClusterClient[T]) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	opts.Watch = true
	return c.Fake.
		InvokesWatch(kcptesting.NewWatchAction(c.resource, logicalcluster.Wildcard, metav1.NamespaceAll, opts))
}

func (c *FakeClusterClient[T]) Kind() schema.GroupVersionKind {
	return c.kind
}

func (c *FakeClusterClient[T]) Resource() schema.GroupVersionResource {
	return c.resource
}
