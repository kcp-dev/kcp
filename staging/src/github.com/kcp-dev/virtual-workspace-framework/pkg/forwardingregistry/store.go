/*
Copyright 2022 The kcp Authors.

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

package forwardingregistry

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	dynamicextension "github.com/kcp-dev/virtual-workspace-framework/pkg/client/dynamic"
)

// StoreFuncs holds proto-functions that can be mutated by successive actors to wrap behavior.
// Ultimately you can pick and choose which functions to expose in the end, depending on how much
// of REST storage you need.
type StoreFuncs struct {
	FactoryFunc
	ListFactoryFunc
	DestroyerFunc

	GetterFunc
	CreaterFunc
	NamedCreaterFunc
	GracefulDeleterFunc
	CollectionDeleterFunc
	ListerFunc
	UpdaterFunc
	WatcherFunc

	TableConvertorFunc
	CategoriesProviderFunc
	ResetFieldsStrategyFunc
}

type Strategy interface {
	rest.RESTCreateStrategy
	rest.ResetFieldsStrategy
}

type DynamicClusterClientFunc func(ctx context.Context) (kcpdynamic.ClusterInterface, error)

// IdentityHashesFunc returns the identity hashes under which the resource is
// stored on this shard at the time of the request. Nil or empty means the
// resource is not served under any identity (only per-cluster requests can
// still be forwarded; wildcard list/watch serve nothing).
//
// A nil IdentityHashesFunc (as opposed to a func returning nil) means the
// resource has no identity at all and is always forwarded under its plain
// resource name; this is what the single-hash constructors use for an empty
// hash.
type IdentityHashesFunc func(ctx context.Context) []string

// identityHashesFromHash maps the legacy single-hash argument to an
// IdentityHashesFunc: an empty hash means "no identity" (nil func), otherwise
// the resource is served under exactly that one hash.
func identityHashesFromHash(apiExportIdentityHash string) IdentityHashesFunc {
	if apiExportIdentityHash == "" {
		return nil
	}
	hashes := []string{apiExportIdentityHash}
	return func(context.Context) []string { return hashes }
}

func DefaultDynamicDelegatedStoreFuncs(
	factory FactoryFunc,
	listFactory ListFactoryFunc,
	destroyerFunc DestroyerFunc,
	strategy Strategy,
	tableConvertor rest.TableConvertor,
	resource schema.GroupVersionResource,
	apiExportIdentityHash string,
	categories []string,
	dynamicClusterClientFunc DynamicClusterClientFunc,
	subResources []string,
	patchConflictRetryBackoff wait.Backoff,
	stopWatchesCh <-chan struct{},
) *StoreFuncs {
	return DefaultDynamicDelegatedStoreFuncsWithIdentities(
		factory, listFactory, destroyerFunc,
		strategy, tableConvertor,
		resource, identityHashesFromHash(apiExportIdentityHash), categories,
		dynamicClusterClientFunc, subResources, patchConflictRetryBackoff, stopWatchesCh,
	)
}

// DefaultDynamicDelegatedStoreFuncsWithIdentities is DefaultDynamicDelegatedStoreFuncs
// for a resource that may be served under a dynamic set of identity hashes
// (see IdentityHashesFunc). Per-cluster requests carry the ":identity" suffix
// only when exactly one hash is returned and otherwise let the shard resolve
// the identity from the binding; wildcard list/watch fan out over all hashes.
func DefaultDynamicDelegatedStoreFuncsWithIdentities(
	factory FactoryFunc,
	listFactory ListFactoryFunc,
	destroyerFunc DestroyerFunc,
	strategy Strategy,
	tableConvertor rest.TableConvertor,
	resource schema.GroupVersionResource,
	identities IdentityHashesFunc,
	categories []string,
	dynamicClusterClientFunc DynamicClusterClientFunc,
	subResources []string,
	patchConflictRetryBackoff wait.Backoff,
	stopWatchesCh <-chan struct{},
) *StoreFuncs {
	client := clientGetter(dynamicClusterClientFunc, strategy.NamespaceScoped(), resource, identities)
	listerWatcher := listerWatcherGetter(dynamicClusterClientFunc, strategy.NamespaceScoped(), resource, identities)
	s := &StoreFuncs{}
	s.FactoryFunc = factory
	s.ListFactoryFunc = listFactory
	s.DestroyerFunc = destroyerFunc
	s.GetterFunc = func(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
		delegate, err := client(ctx)
		if err != nil {
			return nil, err
		}

		return delegate.Get(ctx, name, *options, subResources...)
	}
	s.CreaterFunc = func(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
		unstructuredObj, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("not an Unstructured: %T", obj)
		}

		if err := createValidation(ctx, obj); err != nil {
			return nil, err
		}

		delegate, err := client(ctx)
		if err != nil {
			return nil, err
		}

		return delegate.Create(ctx, unstructuredObj, *options, subResources...)
	}
	s.NamedCreaterFunc = func(ctx context.Context, name string, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
		unstructuredObj, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("not an Unstructured: %T", obj)
		}

		if err := createValidation(ctx, obj); err != nil {
			return nil, err
		}

		delegate, err := client(ctx)
		if err != nil {
			return nil, err
		}

		unstructuredObj = unstructuredObj.DeepCopy()
		unstructuredObj.SetName(name)

		return delegate.Create(ctx, unstructuredObj, *options, subResources...)
	}
	s.GracefulDeleterFunc = func(ctx context.Context, name string, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions) (runtime.Object, bool, error) {
		delegate, err := client(ctx)
		if err != nil {
			return nil, false, err
		}

		preDeleteObj, err := delegate.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, false, err
		}

		err = deleteValidation(ctx, preDeleteObj)
		if err != nil {
			return nil, false, err
		}

		deleter, err := dynamicextension.NewDeleterWithResults(delegate)
		if err != nil {
			return nil, false, err
		}

		obj, status, err := deleter.DeleteWithResult(ctx, name, *options, subResources...)
		if err != nil {
			return nil, false, err
		}

		deletedImmediately := status != http.StatusAccepted

		if obj.GetObjectKind().GroupVersionKind() == metav1.Unversioned.WithKind("Status") {
			// The DELETE request made to the downstream API server can either return the full object,
			// or a Status object, depending on some options and immediate deletion.
			// In the later case, the default encoder does not have the Status kind GVK registered,
			// and fails to serialize it to the response, when it's provided as an unstructured,
			// so we do the conversion upfront.
			status := &metav1.Status{}
			err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.UnstructuredContent(), status)
			return status, deletedImmediately, err
		}

		return obj, deletedImmediately, nil
	}
	s.CollectionDeleterFunc = func(ctx context.Context, deleteValidation rest.ValidateObjectFunc, options *metav1.DeleteOptions, listOptions *metainternalversion.ListOptions) (runtime.Object, error) {
		delegate, err := client(ctx)
		if err != nil {
			return nil, err
		}

		deleter, err := dynamicextension.NewDeleterWithResults(delegate)
		if err != nil {
			return nil, err
		}

		var v1ListOptions metav1.ListOptions
		err = metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(listOptions, &v1ListOptions, nil)
		if err != nil {
			return nil, err
		}

		return deleter.DeleteCollectionWithResult(ctx, *options, v1ListOptions)
	}
	s.ListerFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
		var v1ListOptions metav1.ListOptions
		if err := metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1ListOptions, nil); err != nil {
			return nil, err
		}

		delegates, err := listerWatcher(ctx)
		if err != nil {
			return nil, err
		}

		switch len(delegates) {
		case 0:
			// Wildcard request for a resource that is currently not served
			// under any identity on this shard: nothing to ask the shard for.
			return listFactory(), nil
		case 1:
			list, err := delegates[0].List(ctx, v1ListOptions)
			if apierrors.IsNotFound(err) {
				// The resource (identity) is not served on this shard - e.g. a
				// resource claimed from another APIExport that has no binding on this
				// shard. There is nothing to list here; return an empty list instead
				// of a 404 so wildcard consumers aggregating across shards/endpoints
				// are not wedged by a shard that simply holds none of these objects.
				return listFactory(), nil
			}
			return list, err
		default:
			return listAcrossIdentities(ctx, delegates, v1ListOptions, listFactory)
		}
	}
	s.UpdaterFunc = func(ctx context.Context, name string, objInfo rest.UpdatedObjectInfo, createValidation rest.ValidateObjectFunc, updateValidation rest.ValidateObjectUpdateFunc, forceAllowCreate bool, options *metav1.UpdateOptions) (runtime.Object, bool, error) {
		delegate, err := client(ctx)
		if err != nil {
			return nil, false, err
		}

		requestInfo, _ := genericapirequest.RequestInfoFrom(ctx)

		doUpdate := func() (*unstructured.Unstructured, error) {
			needToCreate := false
			oldObj, err := s.Get(ctx, name, &metav1.GetOptions{})
			if err != nil {
				// Continue on 404 when forceAllowCreate is enabled,
				// or for PATCH requests, so server-side apply requests
				// for non-existent objects can still be processed.
				if !apierrors.IsNotFound(err) ||
					!forceAllowCreate &&
						(requestInfo == nil || requestInfo.Verb != "patch") {
					return nil, err
				}

				// This needs to be the zero value of the object and not nil. This matches the normal rest/storage
				// flows. Additionally, if oldObj were nil, the call the objInfo.UpdatedObject below would return an
				// error because one of the transformers wouldn't be able to extract metadata for oldObj.
				oldObj = &unstructured.Unstructured{}
				needToCreate = true
			}

			// The following call returns a 404 error for non server-side apply
			// requests, i.e., for json, merge and strategic-merge PATCH requests,
			// as it's not possible to construct the updated object out of the patch
			// alone, when the object does not already exist.
			// For server-side apply, the computed object is used as the body of the
			// PUT request below, to create the object from the apply patch.
			obj, err := objInfo.UpdatedObject(ctx, oldObj)
			if err != nil {
				return nil, err
			}

			unstructuredObj, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return nil, fmt.Errorf("not an Unstructured: %T", obj)
			}

			if needToCreate {
				// The object does not currently exist.
				// We switch to calling a create operation on the forwarding registry.
				// This enables support for server-side apply requests, to create non-existent objects.
				//
				// Before creating the object, we want to run the admission chain (including the authorization)
				// to make sure that we're allowed to create objects.
				if err := createValidation(ctx, obj); err != nil {
					return nil, err
				}

				return delegate.Create(ctx, unstructuredObj, updateToCreateOptions(options), subResources...)
			}

			if err := updateValidation(ctx, obj, oldObj); err != nil {
				return nil, err
			}

			return delegate.Update(ctx, unstructuredObj, *options, subResources...)
		}

		if requestInfo != nil && requestInfo.Verb == "patch" {
			var result *unstructured.Unstructured
			err := retry.RetryOnConflict(patchConflictRetryBackoff, func() error {
				var err error
				result, err = doUpdate()
				return err
			})
			return result, false, err
		}

		result, err := doUpdate()
		return result, false, err
	}

	s.WatcherFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
		var v1ListOptions metav1.ListOptions
		if err := metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &v1ListOptions, nil); err != nil {
			return nil, err
		}
		delegates, err := listerWatcher(ctx)
		if err != nil {
			return nil, err
		}

		watchCtx, cancelFn := context.WithCancel(ctx)
		go func() {
			select {
			case <-stopWatchesCh:
				cancelFn()
			case <-ctx.Done():
				return
			}
		}()

		switch len(delegates) {
		case 0:
			// See ListerFunc: the resource is currently served under no
			// identity on this shard.
			return emptyWatchOrNotFound(watchCtx, v1ListOptions, resource.GroupResource())
		case 1:
			w, err := delegates[0].Watch(watchCtx, v1ListOptions)
			if apierrors.IsNotFound(err) {
				if ptr.Deref(v1ListOptions.SendInitialEvents, false) {
					// A WatchList client treats the stream as its initial list and
					// waits for the server to close that list with an
					// "initial-events-end" bookmark before it considers its store
					// synced. An empty watch sends no such bookmark, and never
					// errors either, so there is nothing to make the client relist:
					// it waits forever. Surface the NotFound instead, which the
					// client retries, picking the resource up once this shard
					// serves it.
					return nil, err
				}
				// See ListerFunc: the resource is not served on this shard. Return a
				// watch that yields no events and stays open until the (request- or
				// stop-bounded) context is done, instead of surfacing a 404. Objects
				// that later appear on this shard are picked up on the next relist.
				return newEmptyWatch(watchCtx), nil
			}
			return w, err
		default:
			return watchAcrossIdentities(watchCtx, delegates, v1ListOptions, resource.GroupResource())
		}
	}
	s.TableConvertorFunc = tableConvertor.ConvertToTable
	s.CategoriesProviderFunc = func() []string {
		return categories
	}
	s.ResetFieldsStrategyFunc = strategy.GetResetFields
	return s
}

// forwardedResources returns the resource names (with or without the
// ":identity" suffix) a request must be forwarded to on the shard.
//
// Without identities (nil func) the plain resource is used. Per-cluster
// requests use the suffix only when exactly one hash is served; otherwise the
// plain resource is forwarded and the shard resolves the identity from the
// APIBinding in the target cluster. Wildcard requests get one entry per hash,
// which may be none when nothing is served under any identity here.
func forwardedResources(ctx context.Context, resource schema.GroupVersionResource, identities IdentityHashesFunc, wildcard bool) []schema.GroupVersionResource {
	if identities == nil {
		return []schema.GroupVersionResource{resource}
	}
	hashes := identities(ctx)
	if !wildcard && len(hashes) != 1 {
		return []schema.GroupVersionResource{resource}
	}
	gvrs := make([]schema.GroupVersionResource, 0, len(hashes))
	for _, hash := range hashes {
		gvr := resource
		gvr.Resource += ":" + hash
		gvrs = append(gvrs, gvr)
	}
	return gvrs
}

func clientGetter(dynamicClusterClientFunc DynamicClusterClientFunc, namespaceScoped bool, resource schema.GroupVersionResource, identities IdentityHashesFunc) func(ctx context.Context) (dynamic.ResourceInterface, error) {
	return func(ctx context.Context) (dynamic.ResourceInterface, error) {
		cluster, err := genericapirequest.ValidClusterFrom(ctx)
		if err != nil {
			return nil, apiErrorBadRequest(err)
		}

		// Per-cluster semantics apply to every verb going through the
		// resource client, so a single resource name is always returned.
		gvr := forwardedResources(ctx, resource, identities, false)[0]
		clusterName := cluster.Name

		dynamicClusterClient, err := dynamicClusterClientFunc(ctx)
		if err != nil {
			return nil, fmt.Errorf("error generating dynamic client: %w", err)
		}

		if namespaceScoped {
			if namespace, ok := genericapirequest.NamespaceFrom(ctx); ok {
				return dynamicClusterClient.Cluster(clusterName.Path()).Resource(gvr).Namespace(namespace), nil
			} else {
				return nil, apiErrorBadRequest(fmt.Errorf("there should be a Namespace context in a request for a namespaced resource: %s", gvr.String()))
			}
		} else {
			return dynamicClusterClient.Cluster(clusterName.Path()).Resource(gvr), nil
		}
	}
}

type listerWatcher interface {
	List(ctx context.Context, opts metav1.ListOptions) (*unstructured.UnstructuredList, error)
	Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

// listerWatcherGetter returns the lister/watchers a list or watch request must
// be forwarded to: exactly one for per-cluster requests, and one per served
// identity (possibly none) for wildcard requests.
func listerWatcherGetter(dynamicClusterClientFunc DynamicClusterClientFunc, namespaceScoped bool, resource schema.GroupVersionResource, identities IdentityHashesFunc) func(ctx context.Context) ([]listerWatcher, error) {
	return func(ctx context.Context) ([]listerWatcher, error) {
		cluster, err := genericapirequest.ValidClusterFrom(ctx)
		if err != nil {
			return nil, apiErrorBadRequest(err)
		}
		gvrs := forwardedResources(ctx, resource, identities, cluster.Wildcard)
		namespace, namespaceSet := genericapirequest.NamespaceFrom(ctx)

		dynamicClusterClient, err := dynamicClusterClientFunc(ctx)
		if err != nil {
			return nil, fmt.Errorf("error generating dynamic client: %w", err)
		}

		switch {
		case cluster.Wildcard:
			if namespaceScoped && namespaceSet && namespace != metav1.NamespaceAll {
				return nil, apiErrorBadRequest(fmt.Errorf("cross-cluster LIST and WATCH are required to be cross-namespace, not scoped to namespace %s", namespace))
			}
			delegates := make([]listerWatcher, 0, len(gvrs))
			for _, gvr := range gvrs {
				delegates = append(delegates, dynamicClusterClient.Resource(gvr))
			}
			return delegates, nil
		default:
			gvr := gvrs[0]
			if namespaceScoped {
				if !namespaceSet {
					return nil, apiErrorBadRequest(fmt.Errorf("there should be a Namespace context in a request for a namespaced resource: %s", gvr.String()))
				}
				return []listerWatcher{dynamicClusterClient.Cluster(cluster.Name.Path()).Resource(gvr).Namespace(namespace)}, nil
			}
			return []listerWatcher{dynamicClusterClient.Cluster(cluster.Name.Path()).Resource(gvr)}, nil
		}
	}
}

// listAcrossIdentities lists from every delegate (one per identity hash) and
// returns the concatenation, in delegate order. Pagination cannot span
// several etcd prefixes, so Limit/Continue are dropped and no continue token
// is returned. The result's resourceVersion is the largest one seen: all
// prefixes live in the same etcd on the shard, so revisions are comparable.
// A NotFound from a single delegate means that identity is not served on
// this shard and is skipped, as in the single-identity case.
func listAcrossIdentities(ctx context.Context, delegates []listerWatcher, opts metav1.ListOptions, listFactory ListFactoryFunc) (runtime.Object, error) {
	opts.Limit = 0
	opts.Continue = ""

	var merged *unstructured.UnstructuredList
	var resourceVersions []string
	for _, delegate := range delegates {
		list, err := delegate.List(ctx, opts)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if merged == nil {
			merged = list
		} else {
			merged.Items = append(merged.Items, list.Items...)
		}
		resourceVersions = append(resourceVersions, list.GetResourceVersion())
	}
	if merged == nil {
		return listFactory(), nil
	}

	merged.SetResourceVersion(maxResourceVersion(resourceVersions))
	merged.SetContinue("")
	merged.SetRemainingItemCount(nil)
	return merged, nil
}

// maxResourceVersion returns the numerically largest of the given resource
// versions. If any non-empty one does not parse as an integer, the first
// non-empty resource version is returned instead.
func maxResourceVersion(resourceVersions []string) string {
	first := ""
	maxRV := ""
	var maxValue uint64
	for _, rv := range resourceVersions {
		if rv == "" {
			continue
		}
		if first == "" {
			first = rv
		}
		value, err := strconv.ParseUint(rv, 10, 64)
		if err != nil {
			return first
		}
		if maxRV == "" || value > maxValue {
			maxRV, maxValue = rv, value
		}
	}
	return maxRV
}

// watchAcrossIdentities opens one watch per delegate (one per identity hash)
// and fans them into a single watch.Interface. Delegates answering NotFound
// are skipped as in the single-identity case; if none is left, an empty watch
// bound to ctx is returned. Any other error stops the watches already opened
// and is returned.
func watchAcrossIdentities(ctx context.Context, delegates []listerWatcher, opts metav1.ListOptions, gr schema.GroupResource) (watch.Interface, error) {
	sources := make([]watch.Interface, 0, len(delegates))
	for _, delegate := range delegates {
		w, err := delegate.Watch(ctx, opts)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			for _, s := range sources {
				s.Stop()
			}
			return nil, err
		}
		sources = append(sources, w)
	}

	switch len(sources) {
	case 0:
		return emptyWatchOrNotFound(ctx, opts, gr)
	case 1:
		return sources[0], nil
	default:
		// Each source ends its initial list with its own "initial-events-end"
		// bookmark. Forwarding the first one would tell a WatchList client it
		// is synced while other identities are still sending initial events,
		// silently losing those objects, so the fan-in withholds the bookmark
		// until every source has sent one.
		return newFanInWatch(ctx, sources, ptr.Deref(opts.SendInitialEvents, false)), nil
	}
}

// emptyWatchOrNotFound returns the watch to use when this shard serves the
// resource under no identity at all. A WatchList client must not be given an
// empty watch: it would wait forever for an "initial-events-end" bookmark that
// never arrives. Surfacing NotFound makes it retry instead. See the
// single-identity branch of WatcherFunc for the full reasoning.
func emptyWatchOrNotFound(ctx context.Context, opts metav1.ListOptions, gr schema.GroupResource) (watch.Interface, error) {
	if ptr.Deref(opts.SendInitialEvents, false) {
		return nil, apierrors.NewNotFound(gr, "")
	}
	return newEmptyWatch(ctx), nil
}

// fanInWatch merges the events of several source watches into one result
// channel. It stops - stopping every source and closing the result channel
// exactly once - when Stop is called, when its context is done, or when any
// source closes its own result channel (so the consumer relists instead of
// silently missing that source's events).
type fanInWatch struct {
	sources []watch.Interface
	result  chan watch.Event

	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	// coalesceInitialEvents is set for a WatchList request. Each source ends
	// its initial list with its own "initial-events-end" bookmark; only the
	// last one may reach the client, so that the client is told it is synced
	// once every identity has delivered its initial events.
	coalesceInitialEvents bool
	initialEventsMu       sync.Mutex
	initialEventsSeen     int
}

func newFanInWatch(ctx context.Context, sources []watch.Interface, coalesceInitialEvents bool) *fanInWatch {
	w := &fanInWatch{
		sources:               sources,
		result:                make(chan watch.Event),
		done:                  make(chan struct{}),
		coalesceInitialEvents: coalesceInitialEvents,
	}

	w.wg.Add(len(sources))
	for _, source := range sources {
		go w.forward(source)
	}

	go func() {
		select {
		case <-ctx.Done():
			w.Stop()
		case <-w.done:
		}
	}()

	go func() {
		// Only close the result channel once no forwarder can send anymore.
		w.wg.Wait()
		close(w.result)
	}()

	return w
}

func (w *fanInWatch) forward(source watch.Interface) {
	defer w.wg.Done()
	for {
		select {
		case <-w.done:
			return
		case event, ok := <-source.ResultChan():
			if !ok {
				w.Stop()
				return
			}
			if w.withholdInitialEventsEnd(event) {
				continue
			}
			select {
			case w.result <- event:
			case <-w.done:
				return
			}
		}
	}
}

// withholdInitialEventsEnd reports whether event is an "initial-events-end"
// bookmark that must not be forwarded yet. On a WatchList request every source
// sends one when it finishes its initial list; the client treats the first one
// it sees as "my store is synced", so only the last may pass. Any other event,
// and every event once all sources have reported, is forwarded untouched.
func (w *fanInWatch) withholdInitialEventsEnd(event watch.Event) bool {
	if !w.coalesceInitialEvents || event.Type != watch.Bookmark {
		return false
	}
	accessor, err := meta.Accessor(event.Object)
	if err != nil {
		return false
	}
	if _, ok := accessor.GetAnnotations()[metav1.InitialEventsAnnotationKey]; !ok {
		return false
	}

	w.initialEventsMu.Lock()
	defer w.initialEventsMu.Unlock()
	w.initialEventsSeen++
	// Once every source has reported, this bookmark and any later one are
	// forwarded: the counter only ever grows, so the check stays false.
	return w.initialEventsSeen < len(w.sources)
}

func (w *fanInWatch) Stop() {
	w.stopOnce.Do(func() {
		close(w.done)
		for _, source := range w.sources {
			source.Stop()
		}
	})
}

func (w *fanInWatch) ResultChan() <-chan watch.Event { return w.result }

// emptyWatch is a watch.Interface that produces no events and closes when its
// context is done. It is returned when a forwarded resource is not served on the
// local shard, so wildcard consumers see an empty (but open) watch instead of a
// 404.
type emptyWatch struct {
	ch   chan watch.Event
	once sync.Once
}

func newEmptyWatch(ctx context.Context) *emptyWatch {
	w := &emptyWatch{ch: make(chan watch.Event)}
	go func() {
		<-ctx.Done()
		w.Stop()
	}()
	return w
}

func (w *emptyWatch) Stop() { w.once.Do(func() { close(w.ch) }) }

func (w *emptyWatch) ResultChan() <-chan watch.Event { return w.ch }

// updateToCreateOptions creates a CreateOptions with the same field values as the provided PatchOptions.
func updateToCreateOptions(uo *metav1.UpdateOptions) metav1.CreateOptions {
	co := metav1.CreateOptions{
		DryRun:          uo.DryRun,
		FieldManager:    uo.FieldManager,
		FieldValidation: uo.FieldValidation,
	}
	co.TypeMeta.SetGroupVersionKind(metav1.SchemeGroupVersion.WithKind("CreateOptions"))
	return co
}

// apiErrorBadRequest returns a apierrors.StatusError with a BadRequest reason.
func apiErrorBadRequest(err error) *apierrors.StatusError {
	return &apierrors.StatusError{ErrStatus: metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    http.StatusBadRequest,
		Message: err.Error(),
	}}
}
