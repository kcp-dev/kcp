/*
Copyright 2022 The KCP Authors.

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

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	genericrequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"
)

func (c *controller) reconcile(ctx context.Context, gvrKey string) error {
	// split apart the gvr from the key
	keyParts := strings.Split(gvrKey, "::")
	if len(keyParts) != 2 {
		return fmt.Errorf("incorrect key: %v, expected group.version.resource::key", gvrKey)
	}
	gvrParts := strings.SplitN(keyParts[0], ".", 3)
	gvr := schema.GroupVersionResource{Version: gvrParts[0], Resource: gvrParts[1], Group: gvrParts[2]}
	key := keyParts[1]

	info := c.gvrs[gvr]

	r := &reconciler{
		shardName: c.shardName,
		getLocalCopy: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
			key := kcpcache.ToClusterAwareKey(cluster.String(), namespace, name)
			obj, exists, err := info.local.GetIndexer().GetByKey(key)
			if !exists {
				return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
			} else if err != nil {
				return nil, err // necessary to avoid non-zero nil interface
			}

			u, err := toUnstructured(obj)
			if err != nil {
				return nil, err
			}

			if info.filter != nil && !info.filter(u) {
				return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
			}

			if _, ok := obj.(*unstructured.Unstructured); ok {
				u = u.DeepCopy()
			}
			u.SetKind(info.kind)
			u.SetAPIVersion(gvr.GroupVersion().String())
			return u, nil
		},
		getGlobalCopy: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
			objs, err := info.global.GetIndexer().ByIndex(ByShardAndLogicalClusterAndNamespaceAndName, ShardAndLogicalClusterAndNamespaceKey(c.shardName, cluster, namespace, name))
			if err != nil {
				return nil, err // necessary to avoid non-zero nil interface
			}
			if len(objs) == 0 {
				return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
			} else if len(objs) > 1 {
				return nil, fmt.Errorf("found multiple objects for %v|%v/%v", cluster, namespace, name)
			}

			obj := objs[0]

			u, err := toUnstructured(obj)
			if err != nil {
				return nil, err
			}
			if _, ok := obj.(*unstructured.Unstructured); ok {
				u = u.DeepCopy()
			}

			u.SetKind(info.kind)
			u.SetAPIVersion(gvr.GroupVersion().String())
			return u, nil
		},
		createObject: func(ctx context.Context, cluster logicalcluster.Name, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
			return c.dynamicCacheClient.Cluster(cluster.Path()).Resource(gvr).Namespace(obj.GetNamespace()).Create(ctx, obj, metav1.CreateOptions{})
		},
		updateObject: func(ctx context.Context, cluster logicalcluster.Name, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
			return c.dynamicCacheClient.Cluster(cluster.Path()).Resource(gvr).Namespace(obj.GetNamespace()).Update(ctx, obj, metav1.UpdateOptions{})
		},
		deleteObject: func(ctx context.Context, cluster logicalcluster.Name, ns, name string) error {
			return c.dynamicCacheClient.Cluster(cluster.Path()).Resource(gvr).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
		},
	}
	return r.reconcile(ctx, key)
}

type reconciler struct {
	shardName string

	getLocalCopy  func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error)
	getGlobalCopy func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error)

	createObject func(ctx context.Context, cluster logicalcluster.Name, obj *unstructured.Unstructured) (*unstructured.Unstructured, error)
	updateObject func(ctx context.Context, cluster logicalcluster.Name, obj *unstructured.Unstructured) (*unstructured.Unstructured, error)
	deleteObject func(ctx context.Context, cluster logicalcluster.Name, ns, name string) error
}

// reconcile makes sure that the object under the given key from the local shard is replicated to the cache server.
// the replication function handles the following cases:
//  1. creation of the object in the cache server when the cached object is not found by getGlobalCopy
//  2. deletion of the object from the cache server when the original/local object was removed OR was not found by getLocalCopy
//  3. modification of the cached object to match the original one when meta.annotations, meta.labels, spec or status are different
func (r *reconciler) reconcile(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx).WithValues("reconcilerKey", key)

	clusterName, ns, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}

	localCopy, err := r.getLocalCopy(clusterName, ns, name)
	if err != nil && !apierrors.IsNotFound(err) {
		runtime.HandleError(err)
		return nil
	}
	localExists := !apierrors.IsNotFound(err)

	globalCopy, err := r.getGlobalCopy(clusterName, ns, name)
	if err != nil && !apierrors.IsNotFound(err) {
		runtime.HandleError(err)
		return nil
	}
	globalExists := !apierrors.IsNotFound(err)

	// local is gone or being deleted. Delete in cache.
	if !localExists || !localCopy.GetDeletionTimestamp().IsZero() {
		if !globalExists {
			return nil
		}

		// Object doesn't exist anymore, delete it from the global cache.
		logger.V(2).WithValues("cluster", clusterName, "namespace", ns, "name", name).Info("Deleting object from global cache")
		if err := r.deleteObject(ctx, clusterName, ns, name); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	// local exists, global doesn't. Create in cache.
	if !globalExists {
		// TODO: in the future the original RV will have to be stored in an annotation (?)
		// so that the clients that need to modify the original/local object can do it
		localCopy.SetResourceVersion("")
		annotations := localCopy.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[genericrequest.ShardAnnotationKey] = r.shardName
		localCopy.SetAnnotations(annotations)

		logger.V(2).Info("Creating object in global cache")
		_, err := r.createObject(ctx, clusterName, localCopy)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		return nil
	}

	// update global copy and compare
	metaChanged, err := ensureMeta(globalCopy, localCopy)
	if err != nil {
		return err
	}
	remainingChanged, err := ensureRemaining(globalCopy, localCopy)
	if err != nil {
		return err
	}
	if !metaChanged && !remainingChanged {
		logger.V(4).Info("Object is up to date")
		return nil
	}

	logger.V(2).Info("Updating object in global cache")
	_, err = r.updateObject(ctx, clusterName, globalCopy) // no need for patch because there is only this actor
	return err
}
