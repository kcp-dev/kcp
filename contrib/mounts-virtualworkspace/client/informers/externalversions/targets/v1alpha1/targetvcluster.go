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

	targetsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-virtualworkspace/apis/targets/v1alpha1"
	scopedclientset "github.com/kcp-dev/kcp/contrib/mounts-virtualworkspace/client/clientset/versioned"
	clientset "github.com/kcp-dev/kcp/contrib/mounts-virtualworkspace/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/contrib/mounts-virtualworkspace/client/informers/externalversions/internalinterfaces"
	targetsv1alpha1listers "github.com/kcp-dev/kcp/contrib/mounts-virtualworkspace/client/listers/targets/v1alpha1"
)

// TargetVClusterClusterInformer provides access to a shared informer and lister for
// TargetVClusters.
type TargetVClusterClusterInformer interface {
	Cluster(logicalcluster.Name) TargetVClusterInformer
	Informer() kcpcache.ScopeableSharedIndexInformer
	Lister() targetsv1alpha1listers.TargetVClusterClusterLister
}

type targetVClusterClusterInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

// NewTargetVClusterClusterInformer constructs a new informer for TargetVCluster type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewTargetVClusterClusterInformer(client clientset.ClusterInterface, resyncPeriod time.Duration, indexers cache.Indexers) kcpcache.ScopeableSharedIndexInformer {
	return NewFilteredTargetVClusterClusterInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredTargetVClusterClusterInformer constructs a new informer for TargetVCluster type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredTargetVClusterClusterInformer(client clientset.ClusterInterface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) kcpcache.ScopeableSharedIndexInformer {
	return kcpinformers.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.TargetsV1alpha1().TargetVClusters().List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.TargetsV1alpha1().TargetVClusters().Watch(context.TODO(), options)
			},
		},
		&targetsv1alpha1.TargetVCluster{},
		resyncPeriod,
		indexers,
	)
}

func (f *targetVClusterClusterInformer) defaultInformer(client clientset.ClusterInterface, resyncPeriod time.Duration) kcpcache.ScopeableSharedIndexInformer {
	return NewFilteredTargetVClusterClusterInformer(client, resyncPeriod, cache.Indexers{
		kcpcache.ClusterIndexName: kcpcache.ClusterIndexFunc,
	},
		f.tweakListOptions,
	)
}

func (f *targetVClusterClusterInformer) Informer() kcpcache.ScopeableSharedIndexInformer {
	return f.factory.InformerFor(&targetsv1alpha1.TargetVCluster{}, f.defaultInformer)
}

func (f *targetVClusterClusterInformer) Lister() targetsv1alpha1listers.TargetVClusterClusterLister {
	return targetsv1alpha1listers.NewTargetVClusterClusterLister(f.Informer().GetIndexer())
}

// TargetVClusterInformer provides access to a shared informer and lister for
// TargetVClusters.
type TargetVClusterInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() targetsv1alpha1listers.TargetVClusterLister
}

func (f *targetVClusterClusterInformer) Cluster(clusterName logicalcluster.Name) TargetVClusterInformer {
	return &targetVClusterInformer{
		informer: f.Informer().Cluster(clusterName),
		lister:   f.Lister().Cluster(clusterName),
	}
}

type targetVClusterInformer struct {
	informer cache.SharedIndexInformer
	lister   targetsv1alpha1listers.TargetVClusterLister
}

func (f *targetVClusterInformer) Informer() cache.SharedIndexInformer {
	return f.informer
}

func (f *targetVClusterInformer) Lister() targetsv1alpha1listers.TargetVClusterLister {
	return f.lister
}

type targetVClusterScopedInformer struct {
	factory          internalinterfaces.SharedScopedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

func (f *targetVClusterScopedInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&targetsv1alpha1.TargetVCluster{}, f.defaultInformer)
}

func (f *targetVClusterScopedInformer) Lister() targetsv1alpha1listers.TargetVClusterLister {
	return targetsv1alpha1listers.NewTargetVClusterLister(f.Informer().GetIndexer())
}

// NewTargetVClusterInformer constructs a new informer for TargetVCluster type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewTargetVClusterInformer(client scopedclientset.Interface, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredTargetVClusterInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredTargetVClusterInformer constructs a new informer for TargetVCluster type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredTargetVClusterInformer(client scopedclientset.Interface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.TargetsV1alpha1().TargetVClusters().List(context.TODO(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.TargetsV1alpha1().TargetVClusters().Watch(context.TODO(), options)
			},
		},
		&targetsv1alpha1.TargetVCluster{},
		resyncPeriod,
		indexers,
	)
}

func (f *targetVClusterScopedInformer) defaultInformer(client scopedclientset.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredTargetVClusterInformer(client, resyncPeriod, cache.Indexers{}, f.tweakListOptions)
}
