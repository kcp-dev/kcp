/*
Copyright 2025 The KCP Authors.

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
	"net/http"
	"sync"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/storage"
	storageerrors "k8s.io/apiserver/pkg/storage/errors"
	clientgocache "k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/logicalcluster/v3"

	cachedresourcesreplication "github.com/kcp-dev/kcp/pkg/reconciler/cache/cachedresources/replication"
	"github.com/kcp-dev/kcp/pkg/tombstone"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
	"github.com/kcp-dev/kcp/pkg/virtual/replication/apidomainkey"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

func unwrapCachedObjectWithCluster(obj *cachev1alpha1.CachedObject, cluster logicalcluster.Name) (*unstructured.Unstructured, error) {
	inner, err := unwrapCachedObject(obj)
	if err != nil {
		return nil, err
	}
	setCluster(inner, cluster)

	return inner, nil
}

func unwrapCachedObject(obj *cachev1alpha1.CachedObject) (*unstructured.Unstructured, error) {
	inner := &unstructured.Unstructured{}
	if err := inner.UnmarshalJSON(obj.Spec.Raw.Raw); err != nil {
		return nil, fmt.Errorf("failed to decode inner object: %w", err)
	}
	inner.SetResourceVersion(obj.GetResourceVersion())

	return inner, nil
}

func setCluster(obj *unstructured.Unstructured, cluster logicalcluster.Name) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[logicalcluster.AnnotationKey] = cluster.String()
	obj.SetAnnotations(annotations)
}

func withUnwrapping(apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, localKcpInformers, globalKcpInformers kcpinformers.SharedInformerFactory, cr *cachev1alpha1.CachedResource) forwardingregistry.StorageWrapper {
	wrappedGVR := schema.GroupVersionResource{
		Group:    apiResourceSchema.Spec.Group,
		Version:  version,
		Resource: apiResourceSchema.Spec.Names.Plural,
	}

	namespaced := apiResourceSchema.Spec.Scope == apiextensionsv1.NamespaceScoped

	return forwardingregistry.StorageWrapperFunc(func(resource schema.GroupResource, storage *forwardingregistry.StoreFuncs) {
		storage.GetterFunc = func(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
			parsedKey, err := apidomainkey.Parse(dynamiccontext.APIDomainKeyFrom(ctx))
			if err != nil {
				return nil, fmt.Errorf("invalid API domain key: %v", err)
			}
			targetCluster := genericapirequest.ClusterFrom(ctx)
			if targetCluster.Wildcard {
				panic("### unwrapping cached resource: getter function for wildcard cluster")
			}

			bindings, err := listAPIBindingsByCachedResource(cr.Status.IdentityHash, wrappedGVR.GroupResource(), globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(), localKcpInformers.Apis().V1alpha2().APIBindings())
			if err != nil {
				return nil, fmt.Errorf("### internal error: %v", err)
			}
			clustersForBindings := listClustersInBindings(bindings)
			if !clustersForBindings.Has(targetCluster.Name) {
				return nil, apierrors.NewNotFound(wrappedGVR.GroupResource(), name)
			}

			cachedObjName := cachedresourcesreplication.GenCachedObjectName(wrappedGVR, genericapirequest.NamespaceValue(ctx), name)
			cachedObj, err := globalKcpInformers.Cache().V1alpha1().CachedObjects().Cluster(parsedKey.CachedResourceCluster).Lister().Get(cachedObjName)
			if err != nil {
				return nil, fmt.Errorf("failed to get CachedObject %s for resource %s %s: %v", cachedObjName, wrappedGVR, name, err)
			}

			return unwrapCachedObjectWithCluster(cachedObj, targetCluster.Name)
		}
		storage.WatcherFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
			parsedKey, err := apidomainkey.Parse(dynamiccontext.APIDomainKeyFrom(ctx))
			if err != nil {
				return nil, fmt.Errorf("invalid API domain key: %v", err)
			}

			innerGVR := wrappedGVR
			if innerGVR.Group == "" {
				innerGVR.Group = "core"
			}

			if err := checkCrossNamespaceAndWildcard(ctx, innerGVR, namespaced); err != nil {
				return nil, err
			}

			var listOpts metav1.ListOptions
			if err := metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &listOpts, nil); err != nil {
				return nil, err
			}

			return newUnwrappingWatch(ctx, innerGVR, options, namespaced, genericapirequest.NamespaceValue(ctx),
				globalKcpInformers.Cache().V1alpha1().CachedObjects().Cluster(parsedKey.CachedResourceCluster).Informer(), syntheticClustersProvider(
					*genericapirequest.ClusterFrom(ctx),
					cr.Status.IdentityHash,
					wrappedGVR.GroupResource(),
					localKcpInformers,
					globalKcpInformers,
				))
		}
		storage.ListerFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
			parsedKey, err := apidomainkey.Parse(dynamiccontext.APIDomainKeyFrom(ctx))
			if err != nil {
				return nil, fmt.Errorf("invalid API domain key: %v", err)
			}

			innerGVR := wrappedGVR
			if innerGVR.Group == "" {
				innerGVR.Group = "core"
			}

			if err := checkCrossNamespaceAndWildcard(ctx, innerGVR, namespaced); err != nil {
				return nil, err
			}

			var listOpts metav1.ListOptions
			listOpts.TypeMeta = metav1.TypeMeta{}
			if err := metainternalversion.Convert_internalversion_ListOptions_To_v1_ListOptions(options, &listOpts, nil); err != nil {
				return nil, err
			}

			innerListGVK := schema.GroupVersionKind{
				Group:   wrappedGVR.Group,
				Version: wrappedGVR.Version,
				Kind:    apiResourceSchema.Spec.Names.ListKind,
			}
			if innerListGVK.Kind == "" {
				innerListGVK.Kind = apiResourceSchema.Spec.Names.Kind + "List"
			}

			cachedObjs, err := globalKcpInformers.Cache().V1alpha1().CachedObjects().Informer().GetIndexer().ByIndex(
				cachedresourcesreplication.ByGVRAndLogicalClusterAndNamespace,
				cachedresourcesreplication.GVRAndLogicalClusterAndNamespace(
					innerGVR,
					parsedKey.CachedResourceCluster,
					genericapirequest.NamespaceValue(ctx),
				),
			)
			if err != nil {
				return nil, err
			}

			return newUnwrappingList(innerListGVK, innerGVR.GroupResource(), cachedObjs, options, namespaced, syntheticClustersProvider(
				*genericapirequest.ClusterFrom(ctx),
				cr.Status.IdentityHash,
				wrappedGVR.GroupResource(),
				localKcpInformers,
				globalKcpInformers,
			))
		}
	})
}

// apiErrorBadRequest returns a apierrors.StatusError with a BadRequest reason.
func apiErrorBadRequest(err error) *apierrors.StatusError {
	return &apierrors.StatusError{ErrStatus: metav1.Status{
		Status:  metav1.StatusFailure,
		Code:    http.StatusBadRequest,
		Message: err.Error(),
	}}
}

func checkCrossNamespaceAndWildcard(ctx context.Context, gvr schema.GroupVersionResource, namespaced bool) error {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return apiErrorBadRequest(err)
	}
	namespace, namespaceSet := genericapirequest.NamespaceFrom(ctx)

	if cluster.Wildcard {
		if namespaced && namespaceSet && namespace != metav1.NamespaceAll {
			return apiErrorBadRequest(fmt.Errorf("cross-cluster LIST and WATCH are required to be cross-namespace, not scoped to namespace %s", namespace))
		}
		return nil
	}

	if namespaced {
		if !namespaceSet {
			return apiErrorBadRequest(fmt.Errorf("there should be a Namespace context in a request for a namespaced resource: %s", gvr.String()))
		}
		return nil
	}

	return nil
}

type unwrappingWatch struct {
	lock       sync.Mutex
	doneChan   chan struct{}
	resultChan chan watch.Event

	handler  clientgocache.ResourceEventHandlerRegistration
	informer clientgocache.SharedIndexInformer
}

func newUnwrappingWatch(
	ctx context.Context,
	innerObjGVR schema.GroupVersionResource,
	innerListOpts *metainternalversion.ListOptions,
	namespaced bool,
	namespace string,
	scopedCachedObjectsInformer clientgocache.SharedIndexInformer,
	syntheticClusters func() []logicalcluster.Name,
) (*unwrappingWatch, error) {
	w := &unwrappingWatch{
		doneChan:   make(chan struct{}),
		resultChan: make(chan watch.Event),
		informer:   scopedCachedObjectsInformer,
	}
	go func() {
		for {
			select {
			case <-w.doneChan:
				// Watch was stopped externally via Stop().
				return
			case <-ctx.Done():
				// Watch was stopped due to context. We also clean up with Stop().
				w.Stop()
				return
			}
		}
	}()

	label := labels.Everything()
	if innerListOpts != nil && innerListOpts.LabelSelector != nil {
		label = innerListOpts.LabelSelector
	}
	field := fields.Everything()
	if innerListOpts != nil && innerListOpts.FieldSelector != nil {
		field = innerListOpts.FieldSelector
	}
	attrFunc := storage.DefaultClusterScopedAttr
	if namespaced && namespace != "" {
		attrFunc = storage.DefaultNamespaceScopedAttr
	}
	unwrapWithMatchingSelectors := func(cachedObj *cachev1alpha1.CachedObject) (*unstructured.Unstructured, error) {
		innerObj, err := unwrapCachedObject(cachedObj)
		if err != nil {
			return nil, fmt.Errorf("failed to decode inner object: %w", err)
		}
		innerLabels, innerFields, err := attrFunc(innerObj)
		if err != nil {
			return nil, fmt.Errorf("failed to get attributes in object: %w", err)
		}
		if !label.Matches(innerLabels) {
			return nil, nil
		}
		if !field.Matches(innerFields) {
			return nil, nil
		}
		return innerObj, nil
	}

	handler, err := scopedCachedObjectsInformer.AddEventHandler(clientgocache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			cachedObj := tombstone.Obj[*cachev1alpha1.CachedObject](obj)
			if cachedObj.GetLabels() == nil {
				return false
			}
			valid := cachedObj.Labels[cachedresourcesreplication.LabelKeyObjectGroup] == innerObjGVR.Group &&
				cachedObj.Labels[cachedresourcesreplication.LabelKeyObjectVersion] == innerObjGVR.Version &&
				cachedObj.Labels[cachedresourcesreplication.LabelKeyObjectResource] == innerObjGVR.Resource
			if namespaced && namespace != "" {
				valid = valid && cachedObj.Labels[cachedresourcesreplication.LabelKeyObjectOriginalNamespace] == namespace
			}
			return valid
		},
		Handler: clientgocache.ResourceEventHandlerDetailedFuncs{
			AddFunc: func(obj interface{}, isInInitialList bool) {
				cachedObj := tombstone.Obj[*cachev1alpha1.CachedObject](obj)
				if isInInitialList {
					if innerListOpts.SendInitialEvents == nil || !*innerListOpts.SendInitialEvents {
						// The user explicitly requests to not send the initial list.
						return
					}
					if cachedObj.GetResourceVersion() < innerListOpts.ResourceVersion {
						// This resource is older than the want we want to start from on isInInitial list replay.
						return
					}
				}

				innerObj, err := unwrapWithMatchingSelectors(cachedObj)
				if err != nil {
					w.resultChan <- watch.Event{
						Type:   watch.Error,
						Object: &apierrors.NewInternalError(err).ErrStatus,
					}
					return
				}
				if innerObj == nil {
					// No match because of selectors.
					return
				}
				for _, cluster := range syntheticClusters() {
					obj := innerObj.DeepCopy()
					setCluster(obj, cluster)
					w.resultChan <- watch.Event{
						Type:   watch.Added,
						Object: obj,
					}
				}
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				cachedObj := tombstone.Obj[*cachev1alpha1.CachedObject](newObj)
				innerObj, err := unwrapWithMatchingSelectors(cachedObj)
				if err != nil {
					w.resultChan <- watch.Event{
						Type:   watch.Error,
						Object: &apierrors.NewInternalError(err).ErrStatus,
					}
					return
				}
				if innerObj == nil {
					return
				}
				for _, cluster := range syntheticClusters() {
					obj := innerObj.DeepCopy()
					setCluster(obj, cluster)
					w.resultChan <- watch.Event{
						Type:   watch.Added,
						Object: obj,
					}
				}
			},
			DeleteFunc: func(obj interface{}) {
				cachedObj := tombstone.Obj[*cachev1alpha1.CachedObject](obj)
				innerObj, err := unwrapWithMatchingSelectors(cachedObj)
				if err != nil {
					w.resultChan <- watch.Event{
						Type:   watch.Error,
						Object: &apierrors.NewInternalError(err).ErrStatus,
					}
					return
				}
				if innerObj == nil {
					// No match because of selectors.
					return
				}
				for _, cluster := range syntheticClusters() {
					obj := innerObj.DeepCopy()
					setCluster(obj, cluster)
					w.resultChan <- watch.Event{
						Type:   watch.Added,
						Object: obj,
					}
				}
			},
		},
	})
	if err != nil {
		return nil, err
	}
	w.handler = handler

	return w, nil
}

func (w *unwrappingWatch) Stop() {
	w.lock.Lock()
	defer w.lock.Unlock()

	select {
	case <-w.doneChan:
	default:
		_ = w.informer.RemoveEventHandler(w.handler)
		close(w.doneChan)
		close(w.resultChan)
	}
}

func (w *unwrappingWatch) ResultChan() <-chan watch.Event {
	return w.resultChan
}

func newUnwrappingList(innerListGVK schema.GroupVersionKind, innerObjGR schema.GroupResource, cachedObjs []interface{}, innerListOpts *metainternalversion.ListOptions, namespaced bool, syntheticClusters func() []logicalcluster.Name) (*unstructured.UnstructuredList, error) {
	innerList := &unstructured.UnstructuredList{}
	innerList.SetGroupVersionKind(innerListGVK)

	label := labels.Everything()
	if innerListOpts != nil && innerListOpts.LabelSelector != nil {
		label = innerListOpts.LabelSelector
	}
	field := fields.Everything()
	if innerListOpts != nil && innerListOpts.FieldSelector != nil {
		field = innerListOpts.FieldSelector
	}
	attrFunc := storage.DefaultClusterScopedAttr
	if namespaced {
		attrFunc = storage.DefaultNamespaceScopedAttr
	}

	clusters := syntheticClusters()

	fmt.Printf("### newUnwrappingList len(cachedObjs)=%d, len(clusters)=%d\n", len(cachedObjs), len(clusters))

	if len(clusters) == 0 {
		return innerList, nil
	}

	latestResourceVersion := "0"

	for i := range cachedObjs {
		item := cachedObjs[i].(*cachev1alpha1.CachedObject)
		innerObj, err := unwrapCachedObject(item)
		if err != nil {
			return nil, fmt.Errorf("failed to unwrap item: %w", err)
		}

		innerLabels, innerFields, err := attrFunc(innerObj)
		if err != nil {
			return nil, storageerrors.InterpretListError(err, innerObjGR)
		}
		if !label.Matches(innerLabels) {
			continue
		}
		if !field.Matches(innerFields) {
			continue
		}

		innerObj.SetResourceVersion(item.GetResourceVersion())
		for _, cluster := range clusters {
			obj := innerObj.DeepCopy()
			setCluster(obj, cluster)
			innerList.Items = append(innerList.Items, *obj)
		}

		if innerObj.GetResourceVersion() > latestResourceVersion {
			latestResourceVersion = innerObj.GetResourceVersion()
		}
	}

	innerList.SetResourceVersion(latestResourceVersion)

	return innerList, nil
}

func syntheticClustersProvider(targetCluster genericapirequest.Cluster, identityHash string, wrappedGR schema.GroupResource, localKcpInformers, globalKcpInformers kcpinformers.SharedInformerFactory) func() []logicalcluster.Name {
	return func() []logicalcluster.Name {
		if targetCluster.Wildcard {
			bindings, err := listAPIBindingsByCachedResource(identityHash, wrappedGR, globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(), localKcpInformers.Apis().V1alpha2().APIBindings())
			if err != nil {
				return nil
			}
			clustersForBindings := listClustersInBindings(bindings)
			fmt.Printf("### syntheticClustersProvider, wildcard: %#v\n", sets.List[logicalcluster.Name](clustersForBindings))
			return sets.List[logicalcluster.Name](clustersForBindings)
		} else {
			bindings, err := listAPIBindingsByCachedResource(identityHash, wrappedGR, globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(), localKcpInformers.Apis().V1alpha2().APIBindings())
			if err != nil {
				return nil
			}
			clustersForBindings := listClustersInBindings(bindings)
			if clustersForBindings.Has(targetCluster.Name) {
				fmt.Printf("### syntheticClustersProvider, cluster: %#v\n", sets.List[logicalcluster.Name](clustersForBindings))
				return []logicalcluster.Name{targetCluster.Name}
			}
			fmt.Printf("### syntheticClustersProvider, cluster: nothing ; wanted=%s, have=%#v\n", targetCluster.Name, clustersForBindings.UnsortedList())
			return nil
		}
	}
}
