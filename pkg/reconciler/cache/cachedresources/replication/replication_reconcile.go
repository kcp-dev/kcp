/*
Copyright 2025 The kcp Authors.

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

package replication

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
)

const (
	AnnotationKeyOriginalResourceVersion = "cache.kcp.io/original-resource-version"
	AnnotationKeyOriginalResourceUID     = "cache.kcp.io/original-resource-UID"
)

func (c *Controller) reconcile(ctx context.Context, gvrKey string) error {
	if c.deleted {
		return nil
	}
	// split apart the gvr from the key
	keyParts := strings.Split(gvrKey, "::")
	if len(keyParts) != 2 {
		return fmt.Errorf("incorrect key: %v, expected group.version.resource::key", gvrKey)
	}
	gvrParts := strings.SplitN(keyParts[0], ".", 3)
	gvrFromKey := schema.GroupVersionResource{Version: gvrParts[0], Resource: gvrParts[1], Group: gvrParts[2]}
	gvrWithIdentity := CacheGVRWithIdentity(gvrFromKey, c.replicated.Identity)

	// Key will present in the form of namespace/name in the current logical cluster.
	key := keyParts[1]

	r := &replicationReconciler{
		shardName:          c.shardName,
		localLabelSelector: c.localLabelSelector,
		getLocalPartialObjectMetadata: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
			gvr := gvrFromKey
			key := kcpcache.ToClusterAwareKey(cluster.String(), namespace, name)
			obj, exists, err := c.replicated.Local.GetIndexer().GetByKey(key)
			if !exists {
				return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
			} else if err != nil {
				return nil, err
			}

			u, err := toUnstructured(obj)
			if err != nil {
				return nil, err
			}

			if c.replicated.Filter != nil && !c.replicated.Filter(u) {
				return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
			}

			if _, ok := obj.(*unstructured.Unstructured); ok {
				u = u.DeepCopy()
			}
			u.SetKind(c.replicated.Kind)
			u.SetAPIVersion(gvr.GroupVersion().String())
			return u, nil
		},
		getGlobalPartialObjectMetadata: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
			gvr := gvrFromKey
			key := shardAndLogicalClusterAndNamespaceKey(c.shardName, cluster, namespace, name)

			objs, err := c.replicated.Global.GetIndexer().ByIndex(byShardAndLogicalClusterAndNamespaceAndName, key)
			if err != nil {
				return nil, err
			}
			if len(objs) == 0 {
				return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
			}
			if len(objs) > 1 {
				return nil, fmt.Errorf("found multiple objects for %s", key)
			}
			obj := objs[0]

			u, err := toUnstructured(obj)
			if err != nil {
				return nil, err
			}

			if c.replicated.Filter != nil && !c.replicated.Filter(u) {
				return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
			}

			if _, ok := obj.(*unstructured.Unstructured); ok {
				u = u.DeepCopy()
			}
			u.SetKind(c.replicated.Kind)
			u.SetAPIVersion(gvr.GroupVersion().String())
			return u, nil
		},
		getLocalCopy: func(ctx context.Context, cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
			gvr := gvrFromKey

			obj, err := c.localDynamicClusterClient.Cluster(cluster.Path()).
				Resource(gvrFromKey).
				Namespace(namespace).
				Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}

			obj.SetKind(c.replicated.Kind)
			obj.SetAPIVersion(gvr.GroupVersion().String())
			return obj, nil
		},
		createObjectInCache: func(ctx context.Context, cluster logicalcluster.Name, local *unstructured.Unstructured) (*unstructured.Unstructured, error) {
			return c.globalDynamicClusterClient.
				Cluster(cluster.Path()).
				Resource(gvrWithIdentity).
				Create(cacheclient.WithShardInContext(ctx, shard.Name(c.shardName)), local, metav1.CreateOptions{})
		},
		updateObjectInCache: func(ctx context.Context, cluster logicalcluster.Name, local *unstructured.Unstructured) (*unstructured.Unstructured, error) {
			return c.globalDynamicClusterClient.
				Cluster(cluster.Path()).
				Resource(gvrWithIdentity).
				Update(cacheclient.WithShardInContext(ctx, shard.Name(c.shardName)), local, metav1.UpdateOptions{})
		},
		deleteObjectInCache: func(ctx context.Context, cluster logicalcluster.Name, namespace, name string) error {
			return c.globalDynamicClusterClient.
				Cluster(cluster.Path()).
				Resource(gvrWithIdentity).
				Namespace(namespace).
				Delete(cacheclient.WithShardInContext(ctx, shard.Name(c.shardName)), name, metav1.DeleteOptions{})
		},
	}
	defer c.requeueSelf()
	return r.reconcile(ctx, key)
}

func CacheGVRWithIdentity(gvr schema.GroupVersionResource, identity string) schema.GroupVersionResource {
	cacheGVR := gvr
	cacheGVR.Resource += ":" + identity
	return cacheGVR
}

type replicationReconciler struct {
	shardName          string
	deleted            bool
	localLabelSelector labels.Selector

	getLocalPartialObjectMetadata func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error)
	getLocalCopy                  func(ctx context.Context, cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error)

	getGlobalPartialObjectMetadata func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error)

	createObjectInCache func(ctx context.Context, cluster logicalcluster.Name, local *unstructured.Unstructured) (*unstructured.Unstructured, error)
	updateObjectInCache func(ctx context.Context, cluster logicalcluster.Name, local *unstructured.Unstructured) (*unstructured.Unstructured, error)
	deleteObjectInCache func(ctx context.Context, cluster logicalcluster.Name, namespace, name string) error
}

// reconcile makes sure that the object under the given key from the local shard is replicated to the cache server.
// the replication function handles the following cases:
//  1. creation of the object in the cache server when the cached object is not found by getGlobalCopy
//  2. deletion of the object from the cache server when the original/local object was removed OR was not found by getLocalCopy
//  3. modification of the cached object to match the original one when meta.annotations, meta.labels, spec or status are different
func (r *replicationReconciler) reconcile(ctx context.Context, key string) error {
	if r.deleted {
		return nil
	}
	logger := klog.FromContext(ctx).WithValues("reconcilerKey", key)

	clusterName, ns, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return nil
	}

	// First, get both local and global partial metadata of the object.

	localPartialObjMeta, err := r.getLocalPartialObjectMetadata(clusterName, ns, name)
	if err != nil && !apierrors.IsNotFound(err) {
		utilruntime.HandleError(err)
		return nil
	}
	localExists := !apierrors.IsNotFound(err)

	if localExists && r.localLabelSelector != nil && !r.localLabelSelector.Matches(labels.Set(localPartialObjMeta.GetLabels())) {
		// Exit early: the label selector doesn't match.
		logger.V(2).WithValues("cluster", clusterName, "namespace", ns, "name", name).Info("Object does not match label selector, skipping")
		return nil
	}

	globalPartialObjMeta, err := r.getGlobalPartialObjectMetadata(clusterName, ns, name)
	if err != nil && !apierrors.IsNotFound(err) {
		utilruntime.HandleError(err)
		return nil
	}
	globalExists := !apierrors.IsNotFound(err)

	// Then, ensure that the object is gone from cache if it doesn't exist (anymore) locally.

	if !localExists || !localPartialObjMeta.GetDeletionTimestamp().IsZero() {
		if !globalExists {
			return nil
		}

		if localExists && len(localPartialObjMeta.GetFinalizers()) > 0 {
			// But do respect finalizers. We'll try to delete it on the next iteration.
			return nil
		}

		// Local object doesn't exist anymore, delete it from the global cache.
		logger.V(2).WithValues("cluster", clusterName, "namespace", ns, "name", name).Info("Deleting object from global cache")
		if err := r.deleteObjectInCache(ctx, clusterName, ns, name); err != nil && !apierrors.IsNotFound(err) {
			return err
		}

		return nil
	}

	// Now we know the local object exists. Ensure the object in cache matches the local original.

	if globalExists {
		globalAnnotations := globalPartialObjMeta.GetAnnotations()
		if globalAnnotations != nil && globalAnnotations[AnnotationKeyOriginalResourceVersion] == localPartialObjMeta.GetResourceVersion() {
			// Exit early: there were no changes on the resource.
			logger.V(4).Info("Object is up to date")
			return nil
		}
	}

	// The local DDSIF informer yields only PartialObjectMetadata, and we need the full object for replication.
	localCopy, err := r.getLocalCopy(ctx, clusterName, ns, name)
	if err != nil {
		// Return any error we get. If it's NotFound, we want to requeue in that case too: the local DDSIF
		// informer probably hasn't caught up yet, and we may need to clean up the replicated CachedObject.
		return err
	}

	// Set system annotations on the local copy, so that they are present on the created/updated object replica in cache-server.
	ann := localCopy.GetAnnotations()
	if ann == nil {
		ann = make(map[string]string)
	}
	ann[AnnotationKeyOriginalResourceUID] = string(localCopy.GetUID())
	ann[AnnotationKeyOriginalResourceVersion] = localCopy.GetResourceVersion()
	localCopy.SetAnnotations(ann)

	if !globalExists {
		logger.V(2).WithValues("kind", localPartialObjMeta.GetKind(), "namespace", localPartialObjMeta.GetNamespace(), "name", localPartialObjMeta.GetName()).Info("Creating object in global cache")

		localCopy.SetResourceVersion("")
		_, err := r.createObjectInCache(ctx, clusterName, localCopy)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		return nil
	}

	logger.V(2).WithValues("kind", localPartialObjMeta.GetKind(), "namespace", localPartialObjMeta.GetNamespace(), "name", localPartialObjMeta.GetName()).Info("Updating object in global cache")
	localCopy.SetResourceVersion(globalPartialObjMeta.GetResourceVersion())
	localCopy.SetUID(globalPartialObjMeta.GetUID())
	_, err = r.updateObjectInCache(ctx, clusterName, localCopy)
	return err
}
