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

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apiserver"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
)

// provideShardsRestStorage builds the REST storage for the shards view:
// GET/LIST/WATCH forwarded to the cache server with shard-wildcard scope, so
// a single request returns/streams every shard's Shard object.
func provideShardsRestStorage(
	mainConfig genericapiserver.CompletedConfig,
	cacheDynamicClusterClient kcpdynamic.ClusterInterface,
) (apidefinition.APIDefinition, error) {
	ctx, cancelFn := context.WithCancel(context.Background())

	clientFunc := forwardingregistry.DynamicClusterClientFunc(func(_ context.Context) (kcpdynamic.ClusterInterface, error) {
		return cacheDynamicClusterClient, nil
	})

	restProvider := shardsRestProvider(ctx, clientFunc, withShardsView())

	def, err := apiserver.CreateServingInfoFor(mainConfig, ShardsSchema, corev1alpha1.SchemeGroupVersion.Version, restProvider)
	if err != nil {
		cancelFn()
		return nil, err
	}

	return &apiDefinitionWithCancel{
		APIDefinition: def,
		cancelFn:      cancelFn,
	}, nil
}

// shardsRestProvider is forwardingregistry.ProvideReadOnlyRestStorage
// without APIExport identities: get, list and watch only; everything else
// stays unexposed.
func shardsRestProvider(ctx context.Context, dynamicClusterClientFunc forwardingregistry.DynamicClusterClientFunc, wrapper forwardingregistry.StorageWrapper) apiserver.RestProviderFunc {
	return func(resource schema.GroupVersionResource, kind schema.GroupVersionKind, listKind schema.GroupVersionKind, typer runtime.ObjectTyper, tableConvertor rest.TableConvertor, namespaceScoped bool, schemaValidator validation.SchemaValidator, subresourcesSchemaValidator map[string]validation.SchemaValidator, structuralSchema *structuralschema.Structural) (mainStorage rest.Storage, subresourceStorages map[string]rest.Storage) {
		strategy := customresource.NewStrategy(
			typer,
			namespaceScoped,
			kind,
			forwardingregistry.ValidatePathSegmentName,
			schemaValidator,
			subresourcesSchemaValidator["status"],
			structuralSchema,
			nil, // no status here
			nil, // no scale here
			[]apiextensionsv1.SelectableField{},
		)

		storage, _ := forwardingregistry.NewStorage(
			ctx,
			resource,
			"",
			kind,
			listKind,
			strategy,
			nil,
			tableConvertor,
			nil,
			dynamicClusterClientFunc,
			nil,
			wrapper,
		)

		return &struct {
			forwardingregistry.FactoryFunc
			forwardingregistry.ListFactoryFunc
			forwardingregistry.DestroyerFunc

			forwardingregistry.GetterFunc
			forwardingregistry.ListerFunc
			forwardingregistry.WatcherFunc

			forwardingregistry.TableConvertorFunc
			forwardingregistry.CategoriesProviderFunc
			forwardingregistry.ResetFieldsStrategyFunc
		}{
			FactoryFunc:     storage.FactoryFunc,
			ListFactoryFunc: storage.ListFactoryFunc,
			DestroyerFunc:   storage.DestroyerFunc,

			GetterFunc:  storage.GetterFunc,
			ListerFunc:  storage.ListerFunc,
			WatcherFunc: storage.WatcherFunc,

			TableConvertorFunc:      storage.TableConvertorFunc,
			CategoriesProviderFunc:  storage.CategoriesProviderFunc,
			ResetFieldsStrategyFunc: storage.ResetFieldsStrategyFunc,
		}, nil // no subresources
	}
}

type apiDefinitionWithCancel struct {
	apidefinition.APIDefinition
	cancelFn func()
}

func (d *apiDefinitionWithCancel) TearDown() {
	d.cancelFn()
	d.APIDefinition.TearDown()
}

// sourceContext retargets a request context at the cache server across all
// shards and all logical clusters.
func sourceContext(ctx context.Context) context.Context {
	sourceCtx := genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Wildcard: true})
	return cacheclient.WithShardInContext(sourceCtx, shard.Wildcard)
}

// fixupShard strips cache-server bookkeeping annotations from a returned
// Shard object. The kcp.io/cluster annotation is kept: it tells the consumer
// in which logical cluster the authoritative object lives.
func fixupShard(obj *unstructured.Unstructured) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return
	}
	delete(annotations, shard.AnnotationKey)
	delete(annotations, replication.AnnotationKeyOriginalResourceUID)
	delete(annotations, replication.AnnotationKeyOriginalResourceVersion)
	obj.SetAnnotations(annotations)
}

// withShardsView decorates the StoreFuncs so that every read is served from
// the cache server across all shards, presented as one flat collection.
func withShardsView() forwardingregistry.StorageWrapper {
	return forwardingregistry.StorageWrapperFunc(func(resource schema.GroupResource, storage *forwardingregistry.StoreFuncs) {
		delegateList := storage.ListerFunc
		storage.ListerFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
			result, err := delegateList(sourceContext(ctx), options)
			if err != nil {
				return nil, err
			}

			list := result.(*unstructured.UnstructuredList)
			for i := range list.Items {
				fixupShard(&list.Items[i])
			}
			return list, nil
		}

		storage.GetterFunc = func(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
			// Shard objects live in different logical clusters across shards;
			// a name-only GET cannot be routed to one cluster, and the cache
			// server cannot serve single-key reads across the shard wildcard.
			// That rules out the delegate getter and also a metadata.name
			// field selector, which the generic store turns into a single-key
			// read. List and pick instead - installations have few shards -
			// honouring the requested resource version (GET and LIST share
			// "not older than" semantics).
			listOptions := &metainternalversion.ListOptions{}
			if options != nil {
				listOptions.ResourceVersion = options.ResourceVersion
			}
			result, err := storage.ListerFunc(ctx, listOptions)
			if err != nil {
				return nil, err
			}
			list := result.(*unstructured.UnstructuredList)
			for i := range list.Items {
				if list.Items[i].GetName() == name {
					return &list.Items[i], nil
				}
			}
			return nil, apierrors.NewNotFound(corev1alpha1.Resource("shards"), name)
		}

		delegateWatch := storage.WatcherFunc
		storage.WatcherFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
			w, err := delegateWatch(sourceContext(ctx), options)
			if err != nil {
				return nil, err
			}
			return newFixupWatch(ctx, w), nil
		}
	})
}

// fixupWatch wraps a watch.Interface, stripping cache bookkeeping
// annotations from every event object.
type fixupWatch struct {
	delegate   watch.Interface
	resultChan chan watch.Event
}

func newFixupWatch(ctx context.Context, delegate watch.Interface) *fixupWatch {
	w := &fixupWatch{
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
					fixupShard(obj)
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

func (w *fixupWatch) Stop() {
	w.delegate.Stop()
}

func (w *fixupWatch) ResultChan() <-chan watch.Event {
	return w.resultChan
}
