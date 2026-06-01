/*
Copyright 2026 The kcp Authors.

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

package kubequota

import (
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/controller-manager/pkg/informerfactory"
	"k8s.io/kubernetes/pkg/controller/resourcequota"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/informer"
)

func newNotFound(resource schema.GroupResource, name string) error {
	return apierrors.NewNotFound(resource, name)
}

var _ informerfactory.InformerFactory = (*unscopedInformerFactory)(nil)

// unscopedInformerFactory is a shim  around the cluster-aware informer factory.
// It is used to give the upstream garbage collector cluster-aware
// informers without requiring upstream code changes to informer
// handling.
//
// Similar to the unscopedRQInformer it smuggles the cluster in the namespace.
type unscopedInformerFactory struct {
	factory *informer.DiscoveringDynamicSharedInformerFactory
}

func newUnscopedInformerFactory(f *informer.DiscoveringDynamicSharedInformerFactory) informerfactory.InformerFactory {
	return &unscopedInformerFactory{factory: f}
}

func (f *unscopedInformerFactory) ForResource(resource schema.GroupVersionResource) (informers.GenericInformer, error) {
	clusterInformer, err := f.factory.ForResource(resource)
	if err != nil {
		return nil, err
	}
	return &unscopedGenericInformer{
		clusterInformer: clusterInformer.Informer(),
		resource:        resource,
	}, nil
}

func (f *unscopedInformerFactory) Start(stopCh <-chan struct{}) {
	f.factory.Start(stopCh)
}

type unscopedGenericInformer struct {
	clusterInformer kcpcache.ScopeableSharedIndexInformer
	resource        schema.GroupVersionResource
}

func (i *unscopedGenericInformer) Informer() cache.SharedIndexInformer {
	return &transformingInformer{SharedIndexInformer: i.clusterInformer}
}

func (i *unscopedGenericInformer) Lister() cache.GenericLister {
	return &encodedNamespaceLister{
		indexer:  i.clusterInformer.GetIndexer(),
		resource: i.resource.GroupResource(),
	}
}

// transformingInformer wraps a cache.SharedIndexInformer so that any
// event handler registered through it observes a deep-copied object
// whose metadata.namespace has been rewritten to
// <cluster>|<namespace>. The underlying cache is left untouched.
type transformingInformer struct {
	cache.SharedIndexInformer
}

func (t *transformingInformer) AddEventHandler(handler cache.ResourceEventHandler) (cache.ResourceEventHandlerRegistration, error) {
	return t.SharedIndexInformer.AddEventHandler(wrapHandler(handler))
}

func (t *transformingInformer) AddEventHandlerWithResyncPeriod(handler cache.ResourceEventHandler, resyncPeriod time.Duration) (cache.ResourceEventHandlerRegistration, error) {
	return t.SharedIndexInformer.AddEventHandlerWithResyncPeriod(wrapHandler(handler), resyncPeriod)
}

func (t *transformingInformer) AddEventHandlerWithOptions(handler cache.ResourceEventHandler, options cache.HandlerOptions) (cache.ResourceEventHandlerRegistration, error) {
	return t.SharedIndexInformer.AddEventHandlerWithOptions(wrapHandler(handler), options)
}

func wrapHandler(h cache.ResourceEventHandler) cache.ResourceEventHandler {
	return &transformingHandler{delegate: h}
}

// transformingHandler implements cache.ResourceEventHandler so that the
// isInInitialList flag from OnAdd is forwarded unchanged to the wrapped
// handler.
type transformingHandler struct {
	delegate cache.ResourceEventHandler
}

func (h *transformingHandler) OnAdd(obj any, isInInitialList bool) {
	h.delegate.OnAdd(transformForHandler(obj), isInInitialList)
}

func (h *transformingHandler) OnUpdate(oldObj, newObj any) {
	h.delegate.OnUpdate(transformForHandler(oldObj), transformForHandler(newObj))
}

func (h *transformingHandler) OnDelete(obj any) {
	h.delegate.OnDelete(transformForHandler(obj))
}

// transformForHandler returns a copy of obj whose metadata.namespace
// has been rewritten to <cluster>|<namespace>. Cluster-scoped objects
// and DeletedFinalStateUnknown markers are returned unchanged.
func transformForHandler(obj interface{}) interface{} {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		d.Obj = transformForHandler(d.Obj)
		return d
	}
	rt, ok := obj.(runtime.Object)
	if !ok {
		return obj
	}
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return obj
	}
	ns := accessor.GetNamespace()
	if ns == "" {
		return obj
	}
	if resourcequota.IsEncodedNamespace(ns) {
		return obj
	}
	cluster := logicalcluster.From(accessor)
	if cluster.Empty() {
		return obj
	}
	cp := rt.DeepCopyObject()
	cpAcc, err := meta.Accessor(cp)
	if err != nil {
		return obj
	}
	cpAcc.SetNamespace(resourcequota.EncodeNamespace(cluster, ns))
	return cp
}

// encodedNamespaceLister is a cache.GenericLister that decodes
// <cluster>|<namespace> lookups into cluster+namespace queries against
// the kcp cluster-aware indexer. The list path is unused by the
// kubequota singleton (the QuotaMonitor only calls ByNamespace) but is
// implemented for completeness.
type encodedNamespaceLister struct {
	indexer  cache.Indexer
	resource schema.GroupResource
}

func (l *encodedNamespaceLister) List(selector labels.Selector) ([]runtime.Object, error) {
	var ret []runtime.Object
	err := cache.ListAll(l.indexer, selector, func(m interface{}) {
		if obj, ok := m.(runtime.Object); ok {
			ret = append(ret, obj)
		}
	})
	return ret, err
}

func (l *encodedNamespaceLister) Get(name string) (runtime.Object, error) {
	obj, exists, err := l.indexer.GetByKey(name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, newNotFound(l.resource, name)
	}
	return obj.(runtime.Object), nil
}

func (l *encodedNamespaceLister) ByNamespace(encoded string) cache.GenericNamespaceLister {
	cluster, namespace := resourcequota.ParseEncodedNamespace(encoded)
	return &encodedNamespaceNamespaceLister{
		indexer:   l.indexer,
		cluster:   cluster,
		namespace: namespace,
		resource:  l.resource,
	}
}

type encodedNamespaceNamespaceLister struct {
	indexer   cache.Indexer
	cluster   logicalcluster.Name
	namespace string
	resource  schema.GroupResource
}

func (l *encodedNamespaceNamespaceLister) List(selector labels.Selector) ([]runtime.Object, error) {
	var ret []runtime.Object
	err := kcpcache.ListAllByClusterAndNamespace(l.indexer, l.cluster, l.namespace, selector, func(m interface{}) {
		if obj, ok := m.(runtime.Object); ok {
			ret = append(ret, obj)
		}
	})
	return ret, err
}

func (l *encodedNamespaceNamespaceLister) Get(name string) (runtime.Object, error) {
	key := kcpcache.ToClusterAwareKey(l.cluster.String(), l.namespace, name)
	obj, exists, err := l.indexer.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, newNotFound(l.resource, name)
	}
	return obj.(runtime.Object), nil
}
