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

package builder

import (
	"context"
	"fmt"
	"sync"

	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apiserver"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry"

	configshard "github.com/kcp-dev/kcp/config/shard"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
)

// provideAPIIdentitiesRestStorage builds the REST storage for the identity
// registry view: GET/LIST/WATCH forwarded to the registry scope of the cache
// server, where the APIExport controllers of every shard record the
// identities they assign. The view is read-only: the entries are derived
// state, written by kcp only.
func provideAPIIdentitiesRestStorage(
	mainConfig genericapiserver.CompletedConfig,
	cacheDynamicClusterClient kcpdynamic.ClusterInterface,
) (apidefinition.APIDefinition, error) {
	ctx, cancelFn := context.WithCancel(context.Background())

	clientFunc := forwardingregistry.DynamicClusterClientFunc(func(_ context.Context) (kcpdynamic.ClusterInterface, error) {
		return cacheDynamicClusterClient, nil
	})

	restProvider, err := forwardingregistry.ProvideReadOnlyRestStorage(ctx, clientFunc, withRegistryView(), nil)
	if err != nil {
		cancelFn()
		return nil, err
	}

	def, err := apiserver.CreateServingInfoFor(mainConfig, APIIdentitiesSchema, adminv1alpha1.SchemeGroupVersion.Version, restProvider)
	if err != nil {
		cancelFn()
		return nil, err
	}

	return &apiDefinitionWithCancel{
		APIDefinition: def,
		cancelFn:      cancelFn,
	}, nil
}

// registryContext retargets a request context at the registry scope of the
// cache server: the one shard scope and logical cluster every APIIdentity
// lives under, whichever cluster segment the caller used.
func registryContext(ctx context.Context) context.Context {
	ctx = genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Name: configshard.SystemShardCluster})
	return cacheclient.WithShardInContext(ctx, shard.Registry)
}

// stripCacheBookkeeping removes the cache-server bookkeeping annotations from
// an object read from the cache. The kcp.io/cluster annotation is kept: it
// tells the consumer which logical cluster the object belongs to.
func stripCacheBookkeeping(obj *unstructured.Unstructured) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return
	}
	delete(annotations, shard.AnnotationKey)
	delete(annotations, replication.AnnotationKeyOriginalResourceUID)
	delete(annotations, replication.AnnotationKeyOriginalResourceVersion)
	obj.SetAnnotations(annotations)
}

// withRegistryView decorates the StoreFuncs so that every read is served from
// the registry scope of the cache server, with the cache bookkeeping
// annotations stripped from what is returned.
func withRegistryView() forwardingregistry.StorageWrapper {
	return forwardingregistry.StorageWrapperFunc(func(_ schema.GroupResource, storage *forwardingregistry.StoreFuncs) {
		delegateGet := storage.GetterFunc
		storage.GetterFunc = func(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
			result, err := delegateGet(registryContext(ctx), name, options)
			if err != nil {
				return nil, err
			}
			obj, ok := result.(*unstructured.Unstructured)
			if !ok {
				return nil, fmt.Errorf("expected *unstructured.Unstructured from the registry, got %T", result)
			}
			stripCacheBookkeeping(obj)
			return obj, nil
		}

		delegateList := storage.ListerFunc
		storage.ListerFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
			result, err := delegateList(registryContext(ctx), options)
			if err != nil {
				return nil, err
			}
			list, ok := result.(*unstructured.UnstructuredList)
			if !ok {
				return nil, fmt.Errorf("expected *unstructured.UnstructuredList from the registry, got %T", result)
			}
			for i := range list.Items {
				stripCacheBookkeeping(&list.Items[i])
			}
			return list, nil
		}

		delegateWatch := storage.WatcherFunc
		storage.WatcherFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
			w, err := delegateWatch(registryContext(ctx), options)
			if err != nil {
				return nil, err
			}
			return newStrippingWatch(ctx, w), nil
		}
	})
}

// strippingWatch wraps a watch.Interface, stripping the cache bookkeeping
// annotations from every object it forwards. Bookmarks and errors pass
// through untouched.
type strippingWatch struct {
	delegate   watch.Interface
	resultChan chan watch.Event
	stopOnce   sync.Once
}

func newStrippingWatch(ctx context.Context, delegate watch.Interface) *strippingWatch {
	w := &strippingWatch{
		delegate:   delegate,
		resultChan: make(chan watch.Event, 100), // Matches outgoingBufSize=100 in k8s.io/apiserver/pkg/storage/etcd3/watcher.go.
	}

	go func() {
		defer close(w.resultChan)
		for {
			select {
			case event, ok := <-delegate.ResultChan():
				if !ok {
					return
				}
				if obj, ok := event.Object.(*unstructured.Unstructured); ok {
					stripCacheBookkeeping(obj)
				}
				select {
				case w.resultChan <- event:
				case <-ctx.Done():
					delegate.Stop()
					return
				}
			case <-ctx.Done():
				delegate.Stop()
				return
			}
		}
	}()
	return w
}

func (w *strippingWatch) Stop() {
	w.stopOnce.Do(func() { w.delegate.Stop() })
}

func (w *strippingWatch) ResultChan() <-chan watch.Event {
	return w.resultChan
}
