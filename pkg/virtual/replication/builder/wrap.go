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

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
)

func fixupAnnotations(obj *unstructured.Unstructured, cluster logicalcluster.Name) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	// Make the annotation look like the object is local to the target cluster.
	annotations[logicalcluster.AnnotationKey] = cluster.String()
	delete(annotations, shard.AnnotationKey)
	delete(annotations, replication.AnnotationKeyOriginalResourceUID)
	delete(annotations, replication.AnnotationKeyOriginalResourceVersion)

	obj.SetAnnotations(annotations)
}

// withCachedResource returns a StorageWrapper that annotates each returned item
// with the target cluster from the request context.
func withCachedResource(
	cachedResource *cachev1alpha1.CachedResource,
	export *apisv1alpha2.APIExport,
) forwardingregistry.StorageWrapper {
	// We're guaranteed to get shard name on the cachedResource obj because the APIReconciler uses
	// only the global CachedResources informer, meaning we always go through cache, and the
	// objects are always decorated with shard annotation.
	var shardName shard.Name
	// But should the impossible happen, don't panic on nil annotations...
	if ann := cachedResource.Annotations; ann != nil {
		shardName = shard.Name(cachedResource.Annotations[shard.AnnotationKey])
	}

	sourceCluster := logicalcluster.From(cachedResource)

	return forwardingregistry.StorageWrapperFunc(func(resource schema.GroupResource, storage *forwardingregistry.StoreFuncs) {
		delegateGet := storage.GetterFunc
		storage.GetterFunc = func(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
			targetCluster := genericapirequest.ClusterFrom(ctx)
			if targetCluster.Wildcard {
				return nil, apierrors.NewBadRequest("Wildcard request not supported")
			}

			sourceCtx := genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Name: sourceCluster})
			sourceCtx = cacheclient.WithShardInContext(sourceCtx, shardName)

			obj, err := delegateGet(sourceCtx, name, options)
			if err != nil {
				return nil, err
			}

			u := obj.(*unstructured.Unstructured)
			fixupAnnotations(u, targetCluster.Name)

			return u, nil
		}

		delegateList := storage.ListerFunc
		storage.ListerFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
			targetCluster := genericapirequest.ClusterFrom(ctx)
			if targetCluster.Wildcard {
				return nil, apierrors.NewBadRequest("Wildcard request not supported")
			}

			sourceCtx := genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Name: sourceCluster})
			sourceCtx = cacheclient.WithShardInContext(sourceCtx, shardName)

			result, err := delegateList(sourceCtx, options)
			if err != nil {
				return nil, err
			}

			list := result.(*unstructured.UnstructuredList)
			for i := range list.Items {
				fixupAnnotations(&list.Items[i], targetCluster.Name)
			}

			return list, nil
		}

		delegateWatch := storage.WatcherFunc
		storage.WatcherFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
			targetCluster := genericapirequest.ClusterFrom(ctx)
			if targetCluster.Wildcard {
				return nil, apierrors.NewBadRequest("Wildcard request not supported")
			}

			sourceCtx := genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Name: sourceCluster})
			sourceCtx = cacheclient.WithShardInContext(sourceCtx, shardName)

			w, err := delegateWatch(sourceCtx, options)
			if err != nil {
				return nil, err
			}

			return newScopedAnnotatingWatch(ctx, w, targetCluster.Name), nil
		}
	})
}

// scopedAnnotatingWatch wraps a watch.Interface, setting the cluster annotation on every event object.
type scopedAnnotatingWatch struct {
	delegate   watch.Interface
	resultChan chan watch.Event
	stopOnce   sync.Once
}

func newScopedAnnotatingWatch(ctx context.Context, delegate watch.Interface, cluster logicalcluster.Name) *scopedAnnotatingWatch {
	w := &scopedAnnotatingWatch{
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
					fixupAnnotations(obj, cluster)
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

func (w *scopedAnnotatingWatch) Stop() {
	w.stopOnce.Do(func() { w.delegate.Stop() })
}

func (w *scopedAnnotatingWatch) ResultChan() <-chan watch.Event {
	return w.resultChan
}
