//go:build !ignore_autogenerated
// +build !ignore_autogenerated

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

// Code generated by kcp code-generator. DO NOT EDIT.

package v1alpha1

import (
	"context"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpinformers "github.com/kcp-dev/apimachinery/v2/third_party/informers"
	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	scopedclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/pkg/client/informers/externalversions/internalinterfaces"
	apisv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
)

// APILifecycleClusterInformer provides access to a shared informer and lister for
// APILifecycles.
type APILifecycleClusterInformer interface {
	Cluster(logicalcluster.Name) APILifecycleInformer
	Informer() kcpcache.ScopeableSharedIndexInformer
	Lister() apisv1alpha1listers.APILifecycleClusterLister
}

type aPILifecycleClusterInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

// NewAPILifecycleClusterInformer constructs a new informer for APILifecycle type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewAPILifecycleClusterInformer(client clientset.ClusterInterface, resyncPeriod time.Duration, indexers cache.Indexers) kcpcache.ScopeableSharedIndexInformer {
	return NewFilteredAPILifecycleClusterInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredAPILifecycleClusterInformer constructs a new informer for APILifecycle type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredAPILifecycleClusterInformer(client clientset.ClusterInterface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) kcpcache.ScopeableSharedIndexInformer {
	return kcpinformers.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ApisV1alpha1().APILifecycles().List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ApisV1alpha1().APILifecycles().Watch(context.TODO(), options)
			},
		},
		&apisv1alpha1.APILifecycle{},
		resyncPeriod,
		indexers,
	)
}

func (f *aPILifecycleClusterInformer) defaultInformer(client clientset.ClusterInterface, resyncPeriod time.Duration) kcpcache.ScopeableSharedIndexInformer {
	return NewFilteredAPILifecycleClusterInformer(client, resyncPeriod, cache.Indexers{
		kcpcache.ClusterIndexName: kcpcache.ClusterIndexFunc,
	},
		f.tweakListOptions,
	)
}

func (f *aPILifecycleClusterInformer) Informer() kcpcache.ScopeableSharedIndexInformer {
	return f.factory.InformerFor(&apisv1alpha1.APILifecycle{}, f.defaultInformer)
}

func (f *aPILifecycleClusterInformer) Lister() apisv1alpha1listers.APILifecycleClusterLister {
	return apisv1alpha1listers.NewAPILifecycleClusterLister(f.Informer().GetIndexer())
}

// APILifecycleInformer provides access to a shared informer and lister for
// APILifecycles.
type APILifecycleInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() apisv1alpha1listers.APILifecycleLister
}

func (f *aPILifecycleClusterInformer) Cluster(clusterName logicalcluster.Name) APILifecycleInformer {
	return &aPILifecycleInformer{
		informer: f.Informer().Cluster(clusterName),
		lister:   f.Lister().Cluster(clusterName),
	}
}

type aPILifecycleInformer struct {
	informer cache.SharedIndexInformer
	lister   apisv1alpha1listers.APILifecycleLister
}

func (f *aPILifecycleInformer) Informer() cache.SharedIndexInformer {
	return f.informer
}

func (f *aPILifecycleInformer) Lister() apisv1alpha1listers.APILifecycleLister {
	return f.lister
}

type aPILifecycleScopedInformer struct {
	factory          internalinterfaces.SharedScopedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

func (f *aPILifecycleScopedInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&apisv1alpha1.APILifecycle{}, f.defaultInformer)
}

func (f *aPILifecycleScopedInformer) Lister() apisv1alpha1listers.APILifecycleLister {
	return apisv1alpha1listers.NewAPILifecycleLister(f.Informer().GetIndexer())
}

// NewAPILifecycleInformer constructs a new informer for APILifecycle type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewAPILifecycleInformer(client scopedclientset.Interface, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredAPILifecycleInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredAPILifecycleInformer constructs a new informer for APILifecycle type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredAPILifecycleInformer(client scopedclientset.Interface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ApisV1alpha1().APILifecycles().List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ApisV1alpha1().APILifecycles().Watch(context.TODO(), options)
			},
		},
		&apisv1alpha1.APILifecycle{},
		resyncPeriod,
		indexers,
	)
}

func (f *aPILifecycleScopedInformer) defaultInformer(client scopedclientset.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredAPILifecycleInformer(client, resyncPeriod, cache.Indexers{}, f.tweakListOptions)
}
