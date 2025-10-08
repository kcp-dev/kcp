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

package metadatalister

import (
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/metadata/metadatalister"
	"k8s.io/client-go/tools/cache"
)

// New returns a new ClusterLister.
func New(indexer cache.Indexer, gvr schema.GroupVersionResource) ClusterLister {
	return &metadataClusterLister{indexer: indexer, gvr: gvr}
}

var _ ClusterLister = &metadataClusterLister{}

type metadataClusterLister struct {
	indexer cache.Indexer
	gvr     schema.GroupVersionResource
}

func (l *metadataClusterLister) Cluster(clusterName logicalcluster.Name) metadatalister.Lister {
	return &metadataLister{indexer: l.indexer, gvr: l.gvr, clusterName: clusterName}
}

func (l *metadataClusterLister) List(selector labels.Selector) (ret []*metav1.PartialObjectMetadata, err error) {
	err = cache.ListAll(l.indexer, selector, func(m interface{}) {
		ret = append(ret, m.(*metav1.PartialObjectMetadata))
	})
	return ret, err
}

var _ metadatalister.Lister = &metadataLister{}
var _ metadatalister.NamespaceLister = &metadataNamespaceLister{}

// metadataLister implements the Lister interface.
type metadataLister struct {
	indexer     cache.Indexer
	gvr         schema.GroupVersionResource
	clusterName logicalcluster.Name
}

// List lists all resources in the indexer.
func (l *metadataLister) List(selector labels.Selector) (ret []*metav1.PartialObjectMetadata, err error) {
	selectAll := selector == nil || selector.Empty()

	list, err := l.indexer.ByIndex(kcpcache.ClusterIndexName, kcpcache.ClusterIndexKey(l.clusterName))
	if err != nil {
		return nil, err
	}

	for i := range list {
		obj := list[i].(*metav1.PartialObjectMetadata)
		if selectAll {
			ret = append(ret, obj)
		} else if selector.Matches(labels.Set(obj.GetLabels())) {
			ret = append(ret, obj)
		}
	}

	return ret, err
}

// Get retrieves a resource from the indexer with the given name.
func (l *metadataLister) Get(name string) (*metav1.PartialObjectMetadata, error) {
	key := kcpcache.ToClusterAwareKey(l.clusterName.String(), "", name)
	obj, exists, err := l.indexer.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(l.gvr.GroupResource(), name)
	}
	return obj.(*metav1.PartialObjectMetadata), nil
}

// Namespace returns an object that can list and get resources from a given namespace.
func (l *metadataLister) Namespace(namespace string) metadatalister.NamespaceLister {
	return &metadataNamespaceLister{indexer: l.indexer, namespace: namespace, gvr: l.gvr, clusterName: l.clusterName}
}

// metadataNamespaceLister implements the NamespaceLister interface.
type metadataNamespaceLister struct {
	indexer     cache.Indexer
	namespace   string
	gvr         schema.GroupVersionResource
	clusterName logicalcluster.Name
}

// List lists all resources in the indexer for a given namespace.
func (l *metadataNamespaceLister) List(selector labels.Selector) (ret []*metav1.PartialObjectMetadata, err error) {
	selectAll := selector == nil || selector.Empty()

	var list []interface{}
	if l.namespace == metav1.NamespaceAll {
		list, err = l.indexer.ByIndex(kcpcache.ClusterIndexName, kcpcache.ClusterIndexKey(l.clusterName))
	} else {
		list, err = l.indexer.ByIndex(kcpcache.ClusterAndNamespaceIndexName, kcpcache.ClusterAndNamespaceIndexKey(l.clusterName, l.namespace))
	}
	if err != nil {
		return nil, err
	}

	for i := range list {
		obj := list[i].(*metav1.PartialObjectMetadata)
		if selectAll {
			ret = append(ret, obj)
		} else if selector.Matches(labels.Set(obj.GetLabels())) {
			ret = append(ret, obj)
		}
	}
	return ret, err
}

// Get retrieves a resource from the indexer for a given namespace and name.
func (l *metadataNamespaceLister) Get(name string) (*metav1.PartialObjectMetadata, error) {
	key := kcpcache.ToClusterAwareKey(l.clusterName.String(), l.namespace, name)
	obj, exists, err := l.indexer.GetByKey(key)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.NewNotFound(l.gvr.GroupResource(), name)
	}
	return obj.(*metav1.PartialObjectMetadata), nil
}
