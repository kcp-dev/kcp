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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/validation/field"
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

		storage, _, _ := forwardingregistry.NewStorage(
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

// stripField removes the field at the given path from the object, pruning
// parent maps that end up empty so that comparisons of normalized objects
// converge.
func stripField(obj *unstructured.Unstructured, path []string) {
	unstructured.RemoveNestedField(obj.Object, path...)
	for i := len(path) - 1; i > 0; i-- {
		parent, found, err := unstructured.NestedFieldNoCopy(obj.Object, path[:i]...)
		if err != nil || !found {
			return
		}
		asMap, ok := parent.(map[string]any)
		if !ok || len(asMap) > 0 {
			return
		}
		unstructured.RemoveNestedField(obj.Object, path[:i]...)
	}
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

// mutableField is a field that may be changed on a Shard through the Admin
// workspace, together with the Go type its value must decode into.
type mutableField struct {
	// path is a slice of literal field names, so keys that themselves contain
	// dots - annotation keys - address correctly.
	path []string
	// newValue returns a pointer to a zero value of the field's type.
	newValue func() any
}

// mutableFields are the fields that may be changed on a Shard through the
// Admin workspace: the cordon annotation and the scheduling limits. Everything
// else on a Shard is read-only: shards register themselves and own their
// configuration.
var mutableFields = []mutableField{
	{
		path:     []string{"metadata", "annotations", corev1alpha1.ShardUnschedulableAnnotationKey},
		newValue: func() any { return new(string) },
	},
	{
		path:     []string{"spec", "resourceLimits"},
		newValue: func() any { return new(corev1alpha1.ShardResourceLimits) },
	},
}

// MutableFields are the paths of mutableFields. The replication controller
// carries exactly these paths back cache -> local as its cache-owned fields.
var MutableFields = func() [][]string {
	paths := make([][]string, 0, len(mutableFields))
	for _, f := range mutableFields {
		paths = append(paths, f.path)
	}
	return paths
}()

// mutableFieldNames renders MutableFields for error messages.
func mutableFieldNames() []string {
	names := make([]string, 0, len(MutableFields))
	for _, path := range MutableFields {
		names = append(names, strings.Join(path, "."))
	}
	return names
}

// typedValue decodes a desired value of the field into its Go type and returns
// it re-encoded. The write is forwarded to the cache server, which stores
// whatever it is given, and from there it reaches the owning shard's apiserver,
// which validates against the Shard schema. Decoding here rejects up front -
// with the same strictness as that schema, plus unknown fields - what would
// otherwise be persisted in the cache and then fail to sync, and the
// re-encoding stores the value in the form the type serializes to.
func (f mutableField) typedValue(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	typed := f.newValue()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(typed); err != nil {
		return nil, err
	}
	if raw, err = json.Marshal(typed); err != nil {
		return nil, err
	}
	var canonical any
	if err := utiljson.Unmarshal(raw, &canonical); err != nil {
		return nil, err
	}
	return canonical, nil
}

// mutableFieldUpdate checks that desired differs from current in mutableFields
// only, and returns the desired value of each mutable field - decoded through
// its Go type - along with whether it is present at all: an absent field means
// the admin cleared it. A value that does not fit its type is rejected as
// invalid; any other difference is rejected as forbidden, as shards own the
// rest of their object.
func mutableFieldUpdate(name string, current, desired *unstructured.Unstructured) ([]any, []bool, error) {
	values := make([]any, len(mutableFields))
	present := make([]bool, len(mutableFields))
	normalizedDesired := desired.DeepCopy()
	normalizedCurrent := current.DeepCopy()
	for i, f := range mutableFields {
		path := f.path
		value, found, err := unstructured.NestedFieldNoCopy(desired.Object, path...)
		if err != nil {
			return nil, nil, apierrors.NewBadRequest(fmt.Sprintf("invalid %s: %v", strings.Join(path, "."), err))
		}
		if found {
			if value, err = f.typedValue(value); err != nil {
				return nil, nil, apierrors.NewInvalid(corev1alpha1.Kind("Shard"), name, field.ErrorList{
					field.Invalid(field.NewPath(path[0], path[1:]...), value, err.Error()),
				})
			}
		}
		values[i], present[i] = value, found
		stripField(normalizedDesired, path)
		stripField(normalizedCurrent, path)
	}
	// managedFields bookkeeping is stamped by the request's field manager and
	// is not a user-intended change.
	unstructured.RemoveNestedField(normalizedDesired.Object, "metadata", "managedFields")
	unstructured.RemoveNestedField(normalizedCurrent.Object, "metadata", "managedFields")
	if !equality.Semantic.DeepEqual(normalizedDesired.Object, normalizedCurrent.Object) {
		return nil, nil, apierrors.NewForbidden(corev1alpha1.Resource("shards"), name,
			fmt.Errorf("only %v may be changed through the Admin workspace; Shard objects are owned by the shards themselves", mutableFieldNames()))
	}
	return values, present, nil
}

// withShardsView decorates the StoreFuncs so that every read is served from
// the cache server across all shards, presented as one flat collection, and
// updates - restricted to mutableFields, each decoded through its Go type -
// are applied to the cache copy of the target shard, from where the shard
// hosting the authoritative object picks them up.
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

			values, present, err := mutableFieldUpdate(name, current, desired)
			if err != nil {
				return nil, false, err
			}

			// apply the typed values to the cache copy of the target shard.
			// Parent maps are left in place: the cache bookkeeping annotations
			// below route the write to the owning shard.
			for i, path := range MutableFields {
				if !present[i] {
					unstructured.RemoveNestedField(raw.Object, path...)
					continue
				}
				if err := unstructured.SetNestedField(raw.Object, runtime.DeepCopyJSONValue(values[i]), path...); err != nil {
					return nil, false, err
				}
			}

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

			// Seed the stream with the copies currently in the cache so that
			// the first event for a shard can be resolved against its other
			// copies. The watch is established before the list, so events
			// racing the seed are buffered by the delegate and applied on
			// top of it.
			result, err := delegateList(sourceContext(ctx), &metainternalversion.ListOptions{})
			if err != nil {
				w.Stop()
				return nil, err
			}
			list, ok := result.(*unstructured.UnstructuredList)
			if !ok {
				w.Stop()
				return nil, fmt.Errorf("expected *unstructured.UnstructuredList when seeding the shards watch, got %T", result)
			}

			return newFixupWatch(ctx, w, list.Items), nil
		}
	})
}

// shardCopies holds the cache copies of a single Shard, keyed by the logical
// cluster the copy lives in.
type shardCopies map[logicalcluster.Name]*unstructured.Unstructured

// preferShardCopy reports whether a copy in 'cluster a' should be preferred
// over one in 'cluster b'. The shard-owned copy in system:shard wins, matching
// dedupeShards on the list path; among equals the lowest cluster name keeps
// the choice stable as events arrive in arbitrary order.
func preferShardCopy(a, b logicalcluster.Name) bool {
	if (a == configshard.SystemShardCluster) != (b == configshard.SystemShardCluster) {
		return a == configshard.SystemShardCluster
	}
	return a < b
}

// winner returns the copy that represents the shard, or false when no copy
// is left.
func (c shardCopies) winner() (*unstructured.Unstructured, bool) {
	var best *unstructured.Unstructured
	var bestCluster logicalcluster.Name
	for cluster, obj := range c {
		if best == nil || preferShardCopy(cluster, bestCluster) {
			best, bestCluster = obj, cluster
		}
	}
	return best, best != nil
}

// fixupWatch wraps a watch.Interface, stripping cache bookkeeping
// annotations from every event object and collapsing the stream to one
// object per Shard name, the same way dedupeShards collapses a list.
//
// During mixed-version windows a shard is present in the cache twice: once
// as the shard-owned authoritative object in the shard-local system:shard
// logical cluster, and once as a legacy object in the root workspace.
// Forwarding both copies verbatim breaks every consumer that keys on the
// object name - deleting the legacy copy, as the replication controller does
// while a shard migrates, is then indistinguishable from the shard going
// away, and the shard disappears from the consumer's cache even though the
// authoritative copy is alive. Instead, an event for a losing copy is
// translated into the state of the winning copy, and DELETED is emitted only
// once no copy is left.
type fixupWatch struct {
	delegate   watch.Interface
	resultChan chan watch.Event
	stopOnce   sync.Once

	// copies tracks every cache copy per shard name, and emitted the object
	// last published for that name, so that changes to a losing copy do not
	// reach the client.
	copies  map[string]shardCopies
	emitted map[string]*unstructured.Unstructured
}

func newFixupWatch(ctx context.Context, delegate watch.Interface, seed []unstructured.Unstructured) *fixupWatch {
	w := &fixupWatch{
		delegate:   delegate,
		resultChan: make(chan watch.Event, 100), // Matches outgoingBufSize=100 in k8s.io/apiserver/pkg/storage/etcd3/watcher.go.
		copies:     map[string]shardCopies{},
		emitted:    map[string]*unstructured.Unstructured{},
	}
	for i := range seed {
		w.put(&seed[i])
	}
	// emitted is deliberately left empty: the client listed at its own
	// resourceVersion, so the first event for a shard republishes the
	// winning copy instead of being suppressed as unchanged.

	go func() {
		defer close(w.resultChan)
		for {
			select {
			case event, ok := <-delegate.ResultChan():
				if !ok {
					return
				}
				out, send := w.process(event)
				if !send {
					continue
				}
				select {
				case w.resultChan <- out:
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

// put records obj as the copy of its shard in its logical cluster, with the
// cache bookkeeping annotations stripped.
func (w *fixupWatch) put(obj *unstructured.Unstructured) {
	cluster := logicalcluster.From(obj)
	stored := obj.DeepCopy()
	fixupShard(stored)

	name := stored.GetName()
	copies, found := w.copies[name]
	if !found {
		copies = shardCopies{}
		w.copies[name] = copies
	}
	copies[cluster] = stored
}

// process folds one delegate event into the copy state and returns the event
// to forward, if any.
func (w *fixupWatch) process(event watch.Event) (watch.Event, bool) {
	switch event.Type {
	case watch.Bookmark, watch.Error:
		// A bookmark carries only a resourceVersion and an error carries a
		// Status: neither is a Shard copy, and both must reach the client
		// untouched so that resumption and error reporting keep working.
		return event, true
	}

	obj, ok := event.Object.(*unstructured.Unstructured)
	if !ok {
		return event, true
	}

	name := obj.GetName()
	if event.Type == watch.Deleted {
		if copies, found := w.copies[name]; found {
			delete(copies, logicalcluster.From(obj))
		}
	} else {
		w.put(obj)
	}

	winner, alive := w.copies[name].winner()
	previous, published := w.emitted[name]

	switch {
	case !alive:
		delete(w.copies, name)
		delete(w.emitted, name)
		if !published {
			return watch.Event{}, false
		}
		// Report the object the client was last given: the copy that was
		// actually removed may never have been published to it.
		return watch.Event{Type: watch.Deleted, Object: previous}, true
	case !published:
		w.emitted[name] = winner
		return watch.Event{Type: watch.Added, Object: winner}, true
	case equality.Semantic.DeepEqual(previous, winner):
		// Only a losing copy changed - the shard, as this view presents it,
		// did not.
		return watch.Event{}, false
	default:
		w.emitted[name] = winner
		return watch.Event{Type: watch.Modified, Object: winner}, true
	}
}

func (w *fixupWatch) Stop() {
	w.stopOnce.Do(func() { w.delegate.Stop() })
}

func (w *fixupWatch) ResultChan() <-chan watch.Event {
	return w.resultChan
}
