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

package dynamicinformer

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
	"k8s.io/client-go/dynamic/dynamicinformer"
	upstreaminformers "k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpdynamiclisters "github.com/kcp-dev/client-go/dynamic/dynamiclister"
	kcpinformers "github.com/kcp-dev/client-go/informers"
)

// NewDynamicSharedInformerFactory constructs a new instance of dynamicSharedInformerFactory for all namespaces.
func NewDynamicSharedInformerFactory(client kcpdynamic.ClusterInterface, defaultResync time.Duration) DynamicSharedInformerFactory {
	return NewFilteredDynamicSharedInformerFactory(client, defaultResync, nil)
}

// NewFilteredDynamicSharedInformerFactory constructs a new instance of dynamicSharedInformerFactory.
// Listers obtained via this factory will be subject to the same filters as specified here.
func NewFilteredDynamicSharedInformerFactory(client kcpdynamic.ClusterInterface, defaultResync time.Duration, tweakListOptions dynamicinformer.TweakListOptionsFunc) DynamicSharedInformerFactory {
	return &dynamicSharedInformerFactory{
		client:           client,
		defaultResync:    defaultResync,
		informers:        map[schema.GroupVersionResource]kcpinformers.GenericClusterInformer{},
		startedInformers: make(map[schema.GroupVersionResource]bool),
		tweakListOptions: tweakListOptions,
	}
}

type dynamicSharedInformerFactory struct {
	client        kcpdynamic.ClusterInterface
	defaultResync time.Duration

	lock      sync.Mutex
	informers map[schema.GroupVersionResource]kcpinformers.GenericClusterInformer
	// startedInformers is used for tracking which informers have been started.
	// This allows Start() to be called multiple times safely.
	startedInformers map[schema.GroupVersionResource]bool
	tweakListOptions dynamicinformer.TweakListOptionsFunc
}

var _ DynamicSharedInformerFactory = &dynamicSharedInformerFactory{}

func (f *dynamicSharedInformerFactory) ForResource(gvr schema.GroupVersionResource) kcpinformers.GenericClusterInformer {
	f.lock.Lock()
	defer f.lock.Unlock()

	key := gvr
	informer, exists := f.informers[key]
	if exists {
		return informer
	}

	informer = NewFilteredDynamicInformer(f.client, gvr, f.defaultResync, cache.Indexers{
		kcpcache.ClusterIndexName:             kcpcache.ClusterIndexFunc,
		kcpcache.ClusterAndNamespaceIndexName: kcpcache.ClusterAndNamespaceIndexFunc}, f.tweakListOptions)
	f.informers[key] = informer

	return informer
}

// Start initializes all requested informers.
func (f *dynamicSharedInformerFactory) Start(stopCh <-chan struct{}) {
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
func (f *dynamicSharedInformerFactory) WaitForCacheSync(stopCh <-chan struct{}) map[schema.GroupVersionResource]bool {
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

// NewFilteredDynamicInformer constructs a new informer for a dynamic type.
func NewFilteredDynamicInformer(client kcpdynamic.ClusterInterface, gvr schema.GroupVersionResource, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions dynamicinformer.TweakListOptionsFunc) kcpinformers.GenericClusterInformer {
	return &dynamicClusterInformer{
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

type dynamicClusterInformer struct {
	informer kcpcache.ScopeableSharedIndexInformer
	gvr      schema.GroupVersionResource
}

var _ kcpinformers.GenericClusterInformer = &dynamicClusterInformer{}

func (d *dynamicClusterInformer) Informer() kcpcache.ScopeableSharedIndexInformer {
	return d.informer
}

func (d *dynamicClusterInformer) Lister() kcpcache.GenericClusterLister {
	return kcpdynamiclisters.NewRuntimeObjectShim(kcpdynamiclisters.New(d.informer.GetIndexer(), d.gvr))
}

func (d *dynamicClusterInformer) Cluster(clusterName logicalcluster.Name) upstreaminformers.GenericInformer {
	return d.ClusterWithContext(context.Background(), clusterName)
}

func (d *dynamicClusterInformer) ClusterWithContext(ctx context.Context, clusterName logicalcluster.Name) upstreaminformers.GenericInformer {
	return &dynamicInformer{
		informer: d.Informer().ClusterWithContext(ctx, clusterName),
		lister:   d.Lister().ByCluster(clusterName),
	}
}

type dynamicInformer struct {
	informer cache.SharedIndexInformer
	lister   cache.GenericLister
}

func (d *dynamicInformer) Informer() cache.SharedIndexInformer {
	return d.informer
}

func (d *dynamicInformer) Lister() cache.GenericLister {
	return d.lister
}
