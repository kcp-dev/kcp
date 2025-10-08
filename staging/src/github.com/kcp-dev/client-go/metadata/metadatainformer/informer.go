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

package metadatainformer

import (
	"context"
	"sync"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	thirdpartyinformers "github.com/kcp-dev/apimachinery/v2/third_party/informers"
	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	upstreaminformers "k8s.io/client-go/informers"
	"k8s.io/client-go/metadata/metadatainformer"
	"k8s.io/client-go/tools/cache"

	kcpinformers "github.com/kcp-dev/client-go/informers"
	kcpmetadata "github.com/kcp-dev/client-go/metadata"
	kcpmetadatalisters "github.com/kcp-dev/client-go/metadata/metadatalister"
)

// NewSharedInformerFactory constructs a new instance of sharedInformerFactory for all namespaces.
func NewSharedInformerFactory(client kcpmetadata.ClusterInterface, defaultResync time.Duration) SharedInformerFactory {
	return NewFilteredSharedInformerFactory(client, defaultResync, nil)
}

// NewFilteredSharedInformerFactory constructs a new instance of sharedInformerFactory.
// Listers obtained via this factory will be subject to the same filters as specified here.
func NewFilteredSharedInformerFactory(client kcpmetadata.ClusterInterface, defaultResync time.Duration, tweakListOptions metadatainformer.TweakListOptionsFunc) SharedInformerFactory {
	return &sharedInformerFactory{
		client:           client,
		defaultResync:    defaultResync,
		informers:        map[schema.GroupVersionResource]kcpinformers.GenericClusterInformer{},
		startedInformers: make(map[schema.GroupVersionResource]bool),
		tweakListOptions: tweakListOptions,
	}
}

type sharedInformerFactory struct {
	client        kcpmetadata.ClusterInterface
	defaultResync time.Duration

	lock      sync.Mutex
	informers map[schema.GroupVersionResource]kcpinformers.GenericClusterInformer
	// startedInformers is used for tracking which informers have been started.
	// This allows Start() to be called multiple times safely.
	startedInformers map[schema.GroupVersionResource]bool
	tweakListOptions metadatainformer.TweakListOptionsFunc
}

var _ SharedInformerFactory = &sharedInformerFactory{}

func (f *sharedInformerFactory) ForResource(gvr schema.GroupVersionResource) kcpinformers.GenericClusterInformer {
	f.lock.Lock()
	defer f.lock.Unlock()

	key := gvr
	informer, exists := f.informers[key]
	if exists {
		return informer
	}

	informer = NewFilteredMetadataInformer(f.client, gvr, f.defaultResync, cache.Indexers{
		kcpcache.ClusterIndexName:             kcpcache.ClusterIndexFunc,
		kcpcache.ClusterAndNamespaceIndexName: kcpcache.ClusterAndNamespaceIndexFunc}, f.tweakListOptions)
	f.informers[key] = informer

	return informer
}

// Start initializes all requested informers.
func (f *sharedInformerFactory) Start(stopCh <-chan struct{}) {
	f.lock.Lock()
	defer f.lock.Unlock()

	for informerType, informer := range f.informers {
		if !f.startedInformers[informerType] {
			go informer.Informer().Run(stopCh)
			f.startedInformers[informerType] = true
		}
	}
}

// WaitForCacheSync waits for all started informers' cache were synced.
func (f *sharedInformerFactory) WaitForCacheSync(stopCh <-chan struct{}) map[schema.GroupVersionResource]bool {
	informers := func() map[schema.GroupVersionResource]kcpcache.ScopeableSharedIndexInformer {
		f.lock.Lock()
		defer f.lock.Unlock()

		informers := map[schema.GroupVersionResource]kcpcache.ScopeableSharedIndexInformer{}
		for informerType, informer := range f.informers {
			if f.startedInformers[informerType] {
				informers[informerType] = informer.Informer()
			}
		}
		return informers
	}()

	res := map[schema.GroupVersionResource]bool{}
	for informType, informer := range informers {
		res[informType] = cache.WaitForCacheSync(stopCh, informer.HasSynced)
	}
	return res
}

// NewFilteredMetadataInformer constructs a new informer for a dynamic type.
func NewFilteredMetadataInformer(client kcpmetadata.ClusterInterface, gvr schema.GroupVersionResource, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions metadatainformer.TweakListOptionsFunc) kcpinformers.GenericClusterInformer {
	return &metadataClusterInformer{
		gvr: gvr,
		informer: thirdpartyinformers.NewSharedIndexInformer(
			&cache.ListWatch{
				ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
					if tweakListOptions != nil {
						tweakListOptions(&options)
					}
					return client.Resource(gvr).List(context.TODO(), options)
				},
				WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
					if tweakListOptions != nil {
						tweakListOptions(&options)
					}
					return client.Resource(gvr).Watch(context.TODO(), options)
				},
			},
			&unstructured.Unstructured{},
			resyncPeriod,
			indexers,
		),
	}
}

type metadataClusterInformer struct {
	informer kcpcache.ScopeableSharedIndexInformer
	gvr      schema.GroupVersionResource
}

var _ kcpinformers.GenericClusterInformer = &metadataClusterInformer{}

func (d *metadataClusterInformer) Informer() kcpcache.ScopeableSharedIndexInformer {
	return d.informer
}

func (d *metadataClusterInformer) Lister() kcpcache.GenericClusterLister {
	return kcpmetadatalisters.NewRuntimeObjectShim(kcpmetadatalisters.New(d.informer.GetIndexer(), d.gvr))
}

func (d *metadataClusterInformer) Cluster(clusterName logicalcluster.Name) upstreaminformers.GenericInformer {
	return d.ClusterWithContext(context.Background(), clusterName)
}

func (d *metadataClusterInformer) ClusterWithContext(ctx context.Context, clusterName logicalcluster.Name) upstreaminformers.GenericInformer {
	return &metadataInformer{
		informer: d.Informer().ClusterWithContext(ctx, clusterName),
		lister:   d.Lister().ByCluster(clusterName),
	}
}

type metadataInformer struct {
	informer cache.SharedIndexInformer
	lister   cache.GenericLister
}

func (d *metadataInformer) Informer() cache.SharedIndexInformer {
	return d.informer
}

func (d *metadataInformer) Lister() cache.GenericLister {
	return d.lister
}
