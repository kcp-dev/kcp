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

// APIResourceSchemaClusterInformer provides access to a shared informer and lister for
// APIResourceSchemas.
type APIResourceSchemaClusterInformer interface {
	Cluster(logicalcluster.Name) APIResourceSchemaInformer
	Informer() kcpcache.ScopeableSharedIndexInformer
	Lister() apisv1alpha1listers.APIResourceSchemaClusterLister
}

type aPIResourceSchemaClusterInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

// NewAPIResourceSchemaClusterInformer constructs a new informer for APIResourceSchema type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewAPIResourceSchemaClusterInformer(client clientset.ClusterInterface, resyncPeriod time.Duration, indexers cache.Indexers) kcpcache.ScopeableSharedIndexInformer {
	return NewFilteredAPIResourceSchemaClusterInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredAPIResourceSchemaClusterInformer constructs a new informer for APIResourceSchema type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredAPIResourceSchemaClusterInformer(client clientset.ClusterInterface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) kcpcache.ScopeableSharedIndexInformer {
	return kcpinformers.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ApisV1alpha1().APIResourceSchemas().List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ApisV1alpha1().APIResourceSchemas().Watch(context.TODO(), options)
			},
		},
		&apisv1alpha1.APIResourceSchema{},
		resyncPeriod,
		indexers,
	)
}

func (f *aPIResourceSchemaClusterInformer) defaultInformer(client clientset.ClusterInterface, resyncPeriod time.Duration) kcpcache.ScopeableSharedIndexInformer {
	return NewFilteredAPIResourceSchemaClusterInformer(client, resyncPeriod, cache.Indexers{
		kcpcache.ClusterIndexName: kcpcache.ClusterIndexFunc,
	},
		f.tweakListOptions,
	)
}

func (f *aPIResourceSchemaClusterInformer) Informer() kcpcache.ScopeableSharedIndexInformer {
	return f.factory.InformerFor(&apisv1alpha1.APIResourceSchema{}, f.defaultInformer)
}

func (f *aPIResourceSchemaClusterInformer) Lister() apisv1alpha1listers.APIResourceSchemaClusterLister {
	return apisv1alpha1listers.NewAPIResourceSchemaClusterLister(f.Informer().GetIndexer())
}

// APIResourceSchemaInformer provides access to a shared informer and lister for
// APIResourceSchemas.
type APIResourceSchemaInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() apisv1alpha1listers.APIResourceSchemaLister
}

func (f *aPIResourceSchemaClusterInformer) Cluster(clusterName logicalcluster.Name) APIResourceSchemaInformer {
	return &aPIResourceSchemaInformer{
		informer: f.Informer().Cluster(clusterName),
		lister:   f.Lister().Cluster(clusterName),
	}
}

type aPIResourceSchemaInformer struct {
	informer cache.SharedIndexInformer
	lister   apisv1alpha1listers.APIResourceSchemaLister
}

func (f *aPIResourceSchemaInformer) Informer() cache.SharedIndexInformer {
	return f.informer
}

func (f *aPIResourceSchemaInformer) Lister() apisv1alpha1listers.APIResourceSchemaLister {
	return f.lister
}

type aPIResourceSchemaScopedInformer struct {
	factory          internalinterfaces.SharedScopedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

func (f *aPIResourceSchemaScopedInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&apisv1alpha1.APIResourceSchema{}, f.defaultInformer)
}

func (f *aPIResourceSchemaScopedInformer) Lister() apisv1alpha1listers.APIResourceSchemaLister {
	return apisv1alpha1listers.NewAPIResourceSchemaLister(f.Informer().GetIndexer())
}

// NewAPIResourceSchemaInformer constructs a new informer for APIResourceSchema type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewAPIResourceSchemaInformer(client scopedclientset.Interface, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredAPIResourceSchemaInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredAPIResourceSchemaInformer constructs a new informer for APIResourceSchema type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredAPIResourceSchemaInformer(client scopedclientset.Interface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ApisV1alpha1().APIResourceSchemas().List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ApisV1alpha1().APIResourceSchemas().Watch(context.TODO(), options)
			},
		},
		&apisv1alpha1.APIResourceSchema{},
		resyncPeriod,
		indexers,
	)
}

func (f *aPIResourceSchemaScopedInformer) defaultInformer(client scopedclientset.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredAPIResourceSchemaInformer(client, resyncPeriod, cache.Indexers{}, f.tweakListOptions)
}
