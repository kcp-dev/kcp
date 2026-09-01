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
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apiserver"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry"

	configshard "github.com/kcp-dev/kcp/config/shard"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
)

// provideShardsRestStorage builds the read-only REST storage for the shards
// view: GET/LIST/WATCH forwarded to the cache server with shard-wildcard
// scope, so a single request returns/streams every shard's Shard object.
func provideShardsRestStorage(
	mainConfig genericapiserver.CompletedConfig,
	cacheDynamicClusterClient kcpdynamic.ClusterInterface,
) (apidefinition.APIDefinition, error) {
	ctx, cancelFn := context.WithCancel(context.Background())

	clientFunc := forwardingregistry.DynamicClusterClientFunc(func(_ context.Context) (kcpdynamic.ClusterInterface, error) {
		return cacheDynamicClusterClient, nil
	})

	restProvider, err := forwardingregistry.ProvideReadOnlyRestStorage(
		ctx,
		clientFunc,
		withShardsView(),
		nil,
	)
	if err != nil {
		cancelFn()
		return nil, err
	}

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

// dedupeShards keeps one object per Shard name. During mixed-version
// windows a shard can be present twice in the cache: once from its
// shard-owned authoritative object (in the shard-local system:shard logical
// cluster) and once from a legacy object in the root workspace. The
// shard-owned copy wins.
func dedupeShards(items []unstructured.Unstructured) []unstructured.Unstructured {
	byName := map[string]int{}
	result := make([]unstructured.Unstructured, 0, len(items))
	for i := range items {
		item := items[i]
		name := item.GetName()
		existing, seen := byName[name]
		if !seen {
			byName[name] = len(result)
			result = append(result, item)
			continue
		}
		if logicalcluster.From(&item) == configshard.SystemShardCluster {
			result[existing] = item
		}
	}
	return result
}

// withShardsView decorates the read StoreFuncs so that every request is
// served from the cache server across all shards, presented as one flat
// collection.
func withShardsView() forwardingregistry.StorageWrapper {
	return forwardingregistry.StorageWrapperFunc(func(resource schema.GroupResource, storage *forwardingregistry.StoreFuncs) {
		delegateList := storage.ListerFunc
		storage.ListerFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
			result, err := delegateList(sourceContext(ctx), options)
			if err != nil {
				return nil, err
			}

			list := result.(*unstructured.UnstructuredList)
			list.Items = dedupeShards(list.Items)
			for i := range list.Items {
				fixupShard(&list.Items[i])
			}
			return list, nil
		}

		storage.GetterFunc = func(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
			// Shard objects live in different logical clusters across shards;
			// a name-only GET cannot be routed to one cluster. List and pick
			// instead - installations have few shards.
			result, err := storage.ListerFunc(ctx, &metainternalversion.ListOptions{})
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
// annotations from every event object. Deduplication is not applied to the
// stream: during mixed-version windows a consumer may see events for both
// copies of a shard and must key on the object name.
type fixupWatch struct {
	delegate   watch.Interface
	resultChan chan watch.Event
	stopOnce   sync.Once
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
				if u, ok := event.Object.(*unstructured.Unstructured); ok {
					obj := u.DeepCopy()
					fixupShard(obj)
					event.Object = obj
				}
				w.resultChan <- event
			case <-ctx.Done():
				delegate.Stop()
				return
			}
		}
	}()
	return w
}

func (w *fixupWatch) Stop() {
	w.stopOnce.Do(func() { w.delegate.Stop() })
}

func (w *fixupWatch) ResultChan() <-chan watch.Event {
	return w.resultChan
}
