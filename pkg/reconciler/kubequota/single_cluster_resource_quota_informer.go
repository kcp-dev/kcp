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

package kubequota

import (
	"time"

	"github.com/kcp-dev/logicalcluster"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	coreinformers "k8s.io/client-go/informers/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"

	"github.com/kcp-dev/kcp/pkg/indexers"
)

// SingleClusterResourceQuotaInformer implements ResourceQuotaInformer, scoped to a single logical cluster. Calls to
// Informer().AddEventHandler() and Informer().AddEventHandlerWithResyncPeriod() are forwarded to registerHandler.
type SingleClusterResourceQuotaInformer struct {
	clusterName     logicalcluster.Name
	delegate        coreinformers.ResourceQuotaInformer
	registerHandler func(name logicalcluster.Name, h cache.ResourceEventHandler)
}

// NewSingleClusterResourceQuotaInformer creates a new SingleClusterResourceQuotaInformer.
func NewSingleClusterResourceQuotaInformer(clusterName logicalcluster.Name, delegate coreinformers.ResourceQuotaInformer) *SingleClusterResourceQuotaInformer {
	return &SingleClusterResourceQuotaInformer{
		clusterName: clusterName,
		delegate:    delegate,
	}
}

// Informer returns a cache.SharedIndexInformer that delegates adding event handlers to registerEventHandlerForCluster.
func (s *SingleClusterResourceQuotaInformer) Informer() cache.SharedIndexInformer {
	return &delegatingInformer{
		clusterName:                    s.clusterName,
		SharedIndexInformer:            s.delegate.Informer(),
		registerEventHandlerForCluster: s.registerHandler,
	}
}

// delegatingInformer embeds a cache.SharedIndexInformer, delegating adding event handlers to
// registerEventHandlerForCluster.
type delegatingInformer struct {
	clusterName logicalcluster.Name
	cache.SharedIndexInformer
	registerEventHandlerForCluster func(name logicalcluster.Name, h cache.ResourceEventHandler)
}

// AddEventHandler adds handler with no resync period.
func (d *delegatingInformer) AddEventHandler(handler cache.ResourceEventHandler) {
	d.AddEventHandlerWithResyncPeriod(handler, 0)
}

// AddEventHandlerWithResyncPeriod delegates adding the event handler to registerEventHandlerForCluster. quotaRecalculationPeriod
// is currently ignored.
func (d *delegatingInformer) AddEventHandlerWithResyncPeriod(handler cache.ResourceEventHandler, _ time.Duration) {
	d.registerEventHandlerForCluster(d.clusterName, handler)
}

// Lister returns a ResourceQuotaLister scoped to a single logical cluster.
func (s *SingleClusterResourceQuotaInformer) Lister() corelisters.ResourceQuotaLister {
	return &SingleClusterResourceQuotaLister{
		clusterName: s.clusterName,
		indexer:     s.delegate.Informer().GetIndexer(),
	}
}

// SingleClusterResourceQuotaLister is a ResourceQuotaLister scoped to a single logical cluster.
type SingleClusterResourceQuotaLister struct {
	indexer     cache.Indexer
	clusterName logicalcluster.Name
}

func listByIndex(indexer cache.Indexer, indexName, indexValue string, selector labels.Selector, appendFunc func(obj interface{})) error {
	selectAll := selector == nil || selector.Empty()

	list, err := indexer.ByIndex(indexName, indexValue)
	if err != nil {
		return err
	}

	for i := range list {
		if selectAll {
			appendFunc(list[i])
			continue
		}

		metadata, err := meta.Accessor(list[i])
		if err != nil {
			return err
		}
		if selector.Matches(labels.Set(metadata.GetLabels())) {
			appendFunc(list[i])
		}
	}

	return nil
}

// List lists all ResourceQuota objects in a single logical cluster.
func (s SingleClusterResourceQuotaLister) List(selector labels.Selector) (ret []*corev1.ResourceQuota, err error) {
	if err := listByIndex(s.indexer, indexers.ByLogicalCluster, s.clusterName.String(), selector, func(obj interface{}) {
		ret = append(ret, obj.(*corev1.ResourceQuota))
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

// ResourceQuotas returns a ResourceQuotaNamespaceLister scoped to a single logical cluster.
func (s SingleClusterResourceQuotaLister) ResourceQuotas(namespace string) corelisters.ResourceQuotaNamespaceLister {
	return &singleClusterResourceQuotaNamespaceLister{
		indexer:     s.indexer,
		clusterName: s.clusterName,
		namespace:   namespace,
	}
}

type singleClusterResourceQuotaNamespaceLister struct {
	indexer     cache.Indexer
	clusterName logicalcluster.Name
	namespace   string
}

// List lists all ResourceQuota objects in a single namespace in a single logical cluster.
func (s *singleClusterResourceQuotaNamespaceLister) List(selector labels.Selector) (ret []*corev1.ResourceQuota, err error) {
	indexValue := clusters.ToClusterAwareKey(s.clusterName, s.namespace)
	if err := listByIndex(s.indexer, indexers.ByLogicalClusterAndNamespace, indexValue, selector, func(obj interface{}) {
		ret = append(ret, obj.(*corev1.ResourceQuota))
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

// Get gets the ResourceQuota identified by name in the appropriate namespace and logical cluster.
func (s *singleClusterResourceQuotaNamespaceLister) Get(name string) (*corev1.ResourceQuota, error) {
	// NOTE: DO NOT DO IT LIKE THIS!
	// key := s.namespace + "/" + clusters.ToClusterAwareKey(s.clusterName, name)

	// Normally we would do the above, but this is only used by the upstream resource quota controller where it does
	// the following:
	//
	// 		namespace, name, err := cache.SplitMetaNamespaceKey(key)
	// 		if err != nil {
	// 			return err
	// 		}
	// 		resourceQuota, err := rq.rqLister.ResourceQuotas(namespace).Get(name)
	//
	// And in this case, when you split key into namespace and name, "name" is actually a clusterAwareName, meaning
	// the below construction of key is correct.
	key := s.namespace + "/" + name

	obj, exists, err := s.indexer.GetByKey(key)
	if err != nil {
		return nil, err
	}

	if !exists {
		return nil, errors.NewNotFound(schema.GroupResource{Resource: "resourcequotas"}, name)
	}

	return obj.(*corev1.ResourceQuota), nil
}
