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

package garbagecollector

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/controller-manager/pkg/informerfactory"

	kcpinformers "github.com/kcp-dev/client-go/informers"

	"github.com/kcp-dev/kcp/pkg/informer"
)

var _ informerfactory.InformerFactory = (*unscopedInformerFactory)(nil)

// unscopedInformerFactory is a shim  around the cluster-aware informer factory.
// It is used to give the upstream garbage collector cluster-aware
// informers without requiring upstream code changes to informer
// handling.
type unscopedInformerFactory struct {
	factory *informer.DiscoveringDynamicSharedInformerFactory
}

func newUnscopedInformerFactory(informerFactory *informer.DiscoveringDynamicSharedInformerFactory) informerfactory.InformerFactory {
	return &unscopedInformerFactory{factory: informerFactory}
}

func (f *unscopedInformerFactory) ForResource(resource schema.GroupVersionResource) (informers.GenericInformer, error) {
	clusterInformer, err := f.factory.ForResource(resource)
	if err != nil {
		return nil, err
	}
	return &unscopedGenericInformer{clusterInformer: clusterInformer}, nil
}

func (f *unscopedInformerFactory) Start(stopCh <-chan struct{}) {
	f.factory.Start(stopCh)
}

type unscopedGenericInformer struct {
	clusterInformer kcpinformers.GenericClusterInformer
}

func (i *unscopedGenericInformer) Informer() cache.SharedIndexInformer {
	return i.clusterInformer.Informer()
}

func (i *unscopedGenericInformer) Lister() cache.GenericLister {
	// cache.GenericLister also requires ByNamespace, which the GenericClusterLister
	// does not implement (that'd be .ByCluster->.ByNamespace).
	// But since the garbage collector does not use the lister this can
	// be skipped.
	// As a sanity check throwing a panic - if this should ever change
	// more wrappers have to be implemented.
	panic("garbage collector called .Lister on the unscoped generic informer")
}
