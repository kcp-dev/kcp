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

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource"
	"k8s.io/apimachinery/pkg/api/equality"
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

// provideShardsRestStorage builds the REST storage for the shards view:
// GET/LIST/WATCH forwarded to the cache server with shard-wildcard scope, so
// a single request returns/streams every shard's Shard object, plus UPDATE
// restricted to the allow-listed operational annotations (cordoning), which
// is applied to the cache copy of the target shard and picked up from there
// by the shard hosting the authoritative object.
func provideShardsRestStorage(
	mainConfig genericapiserver.CompletedConfig,
	cacheDynamicClusterClient kcpdynamic.ClusterInterface,
) (apidefinition.APIDefinition, error) {
	ctx, cancelFn := context.WithCancel(context.Background())

	clientFunc := forwardingregistry.DynamicClusterClientFunc(func(_ context.Context) (kcpdynamic.ClusterInterface, error) {
		return cacheDynamicClusterClient, nil
	})

	restProvider := shardsRestProvider(ctx, clientFunc, withShardsView(cacheDynamicClusterClient))

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

// shardsRestProvider is forwardingregistry.ProvideReadOnlyRestStorage plus
// the Updater endpoint (needed for kubectl annotate/patch of the
// allow-listed annotations); everything else stays unexposed.
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
			forwardingregistry.UpdaterFunc

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
			UpdaterFunc: storage.UpdaterFunc,

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

// stripAnnotation removes the given annotation key from the object, dropping
// the annotations map entirely when it ends up empty so that comparisons of
// normalized objects converge.
func stripAnnotation(obj *unstructured.Unstructured, key string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		return
	}
	delete(annotations, key)
	if len(annotations) == 0 {
		unstructured.RemoveNestedField(obj.Object, "metadata", "annotations")
		return
	}
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

// MutableAnnotations are the annotations that may be changed on a Shard
// through the Admin workspace. Everything else on a Shard is read-only:
// shards register themselves and own their configuration.
var MutableAnnotations = []string{corev1alpha1.ShardUnschedulableAnnotationKey}

// withShardsView decorates the StoreFuncs so that every read is served from
// the cache server across all shards, presented as one flat collection, and
// updates - restricted to MutableAnnotations - are applied to the cache copy
// of the target shard, from where the shard hosting the authoritative object
// picks them up.
func withShardsView(cacheDynamicClusterClient kcpdynamic.ClusterInterface) forwardingregistry.StorageWrapper {
	return forwardingregistry.StorageWrapperFunc(func(resource schema.GroupResource, storage *forwardingregistry.StoreFuncs) {
		delegateList := storage.ListerFunc

		// rawShardByName returns the cache copy of the named Shard with its
		// bookkeeping annotations intact, so a write can be routed to the
		// exact cluster and shard the copy lives under.
		rawShardByName := func(ctx context.Context, name string) (*unstructured.Unstructured, error) {
			result, err := delegateList(sourceContext(ctx), &metainternalversion.ListOptions{})
			if err != nil {
				return nil, err
			}
			list := result.(*unstructured.UnstructuredList)
			items := dedupeShards(list.Items)
			for i := range items {
				if items[i].GetName() == name {
					return &items[i], nil
				}
			}
			return nil, apierrors.NewNotFound(corev1alpha1.Resource("shards"), name)
		}

		storage.UpdaterFunc = func(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, _ rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, _ bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
			raw, err := rawShardByName(ctx, name)
			if err != nil {
				return nil, false, err
			}
			current := raw.DeepCopy()
			fixupShard(current)

			updatedObj, err := objInfo.UpdatedObject(ctx, current)
			if err != nil {
				return nil, false, err
			}
			desired, ok := updatedObj.(*unstructured.Unstructured)
			if !ok {
				return nil, false, apierrors.NewBadRequest(fmt.Sprintf("unexpected object type %T", updatedObj))
			}
			if updateValidation != nil {
				if err := updateValidation(ctx, desired, current); err != nil {
					return nil, false, err
				}
			}

			// everything except the mutable annotations must be unchanged.
			values := map[string]*string{}
			normalizedDesired := desired.DeepCopy()
			normalizedCurrent := current.DeepCopy()
			for _, key := range MutableAnnotations {
				if v, ok := desired.GetAnnotations()[key]; ok {
					value := v
					values[key] = &value
				} else {
					values[key] = nil
				}
				stripAnnotation(normalizedDesired, key)
				stripAnnotation(normalizedCurrent, key)
			}
			// managedFields bookkeeping is stamped by the request's field
			// manager and is not a user-intended change.
			unstructured.RemoveNestedField(normalizedDesired.Object, "metadata", "managedFields")
			unstructured.RemoveNestedField(normalizedCurrent.Object, "metadata", "managedFields")
			if !equality.Semantic.DeepEqual(normalizedDesired.Object, normalizedCurrent.Object) {
				return nil, false, apierrors.NewForbidden(corev1alpha1.Resource("shards"), name,
					fmt.Errorf("only the %v annotations may be changed through the Admin workspace; Shard objects are owned by the shards themselves", MutableAnnotations))
			}

			// apply the change to the cache copy of the target shard.
			annotations := raw.GetAnnotations()
			for key, value := range values {
				if value == nil {
					delete(annotations, key)
				} else {
					annotations[key] = *value
				}
			}
			raw.SetAnnotations(annotations)

			targetCtx := cacheclient.WithShardInContext(ctx, shard.Name(raw.GetAnnotations()[shard.AnnotationKey]))
			result, err := cacheDynamicClusterClient.Cluster(logicalcluster.From(raw).Path()).
				Resource(corev1alpha1.SchemeGroupVersion.WithResource("shards")).
				Update(targetCtx, raw, *options)
			if err != nil {
				return nil, false, err
			}
			fixupShard(result)
			return result, false, nil
		}
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
