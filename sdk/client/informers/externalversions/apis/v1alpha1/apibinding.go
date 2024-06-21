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

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	scopedclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned"
	clientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/sdk/client/informers/externalversions/internalinterfaces"
	apisv1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/apis/v1alpha1"
)

// APIBindingClusterInformer provides access to a shared informer and lister for
// APIBindings.
type APIBindingClusterInformer interface {
	Cluster(logicalcluster.Name) APIBindingInformer
	Informer() kcpcache.ScopeableSharedIndexInformer
	Lister() apisv1alpha1listers.APIBindingClusterLister
}

type aPIBindingClusterInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

// NewAPIBindingClusterInformer constructs a new informer for APIBinding type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewAPIBindingClusterInformer(client clientset.ClusterInterface, resyncPeriod time.Duration, indexers cache.Indexers) kcpcache.ScopeableSharedIndexInformer {
	return NewFilteredAPIBindingClusterInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredAPIBindingClusterInformer constructs a new informer for APIBinding type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredAPIBindingClusterInformer(client clientset.ClusterInterface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) kcpcache.ScopeableSharedIndexInformer {
	return kcpinformers.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ApisV1alpha1().APIBindings().List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ApisV1alpha1().APIBindings().Watch(context.TODO(), options)
			},
		},
		&apisv1alpha1.APIBinding{},
		resyncPeriod,
		indexers,
	)
}

func (f *aPIBindingClusterInformer) defaultInformer(client clientset.ClusterInterface, resyncPeriod time.Duration) kcpcache.ScopeableSharedIndexInformer {
	return NewFilteredAPIBindingClusterInformer(client, resyncPeriod, cache.Indexers{
		kcpcache.ClusterIndexName: kcpcache.ClusterIndexFunc,
	},
		f.tweakListOptions,
	)
}

func (f *aPIBindingClusterInformer) Informer() kcpcache.ScopeableSharedIndexInformer {
	return f.factory.InformerFor(&apisv1alpha1.APIBinding{}, f.defaultInformer)
}

func (f *aPIBindingClusterInformer) Lister() apisv1alpha1listers.APIBindingClusterLister {
	return apisv1alpha1listers.NewAPIBindingClusterLister(f.Informer().GetIndexer())
}

// APIBindingInformer provides access to a shared informer and lister for
// APIBindings.
type APIBindingInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() apisv1alpha1listers.APIBindingLister
}

func (f *aPIBindingClusterInformer) Cluster(clusterName logicalcluster.Name) APIBindingInformer {
	return &aPIBindingInformer{
		informer: f.Informer().Cluster(clusterName),
		lister:   f.Lister().Cluster(clusterName),
	}
}

type aPIBindingInformer struct {
	informer cache.SharedIndexInformer
	lister   apisv1alpha1listers.APIBindingLister
}

func (f *aPIBindingInformer) Informer() cache.SharedIndexInformer {
	return f.informer
}

func (f *aPIBindingInformer) Lister() apisv1alpha1listers.APIBindingLister {
	return f.lister
}

type aPIBindingScopedInformer struct {
	factory          internalinterfaces.SharedScopedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

func (f *aPIBindingScopedInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&apisv1alpha1.APIBinding{}, f.defaultInformer)
}

func (f *aPIBindingScopedInformer) Lister() apisv1alpha1listers.APIBindingLister {
	return apisv1alpha1listers.NewAPIBindingLister(f.Informer().GetIndexer())
}

// NewAPIBindingInformer constructs a new informer for APIBinding type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewAPIBindingInformer(client scopedclientset.Interface, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredAPIBindingInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredAPIBindingInformer constructs a new informer for APIBinding type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredAPIBindingInformer(client scopedclientset.Interface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ApisV1alpha1().APIBindings().List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ApisV1alpha1().APIBindings().Watch(context.TODO(), options)
			},
		},
		&apisv1alpha1.APIBinding{},
		resyncPeriod,
		indexers,
	)
}

func (f *aPIBindingScopedInformer) defaultInformer(client scopedclientset.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredAPIBindingInformer(client, resyncPeriod, cache.Indexers{}, f.tweakListOptions)
}