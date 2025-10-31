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

package replication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/martinlindhe/base36"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	genericrequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/cache"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
)

const (
	LabelKeyObjectSchema                 = "cache.kcp.io/object-schema"
	LabelKeyObjectGroup                  = "cache.kcp.io/object-group"
	LabelKeyObjectVersion                = "cache.kcp.io/object-version"
	LabelKeyObjectResource               = "cache.kcp.io/object-resource"
	LabelKeyObjectOriginalName           = "cache.kcp.io/object-original-name"
	LabelKeyObjectOriginalNamespace      = "cache.kcp.io/object-original-namespace"
	AnnotationKeyOriginalResourceVersion = "cache.kcp.io/original-resource-version"
	AnnotationKeyOriginalResourceUID     = "cache.kcp.io/original-resource-UID"
)

func GenCachedObjectName(gvr schema.GroupVersionResource, namespace, name string) string {
	buf := bytes.Buffer{}
	buf.WriteString(gvr.String())
	buf.WriteString(namespace)
	buf.WriteString(name)

	hash := sha256.Sum256(buf.Bytes())
	base36hash := strings.ToLower(base36.EncodeBytes(hash[:]))

	return base36hash
}

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
				return nil, err // necessary to avoid non-zero nil interface
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
		getCachedObject: func(ctx context.Context, cluster logicalcluster.Name, namespace, name string) (*cachev1alpha1.CachedObject, error) {
			gvr := gvrFromKey
			if gvr.Group == "" {
				gvr.Group = "core"
			}

			key := GVRAndShardAndLogicalClusterAndNamespaceKey(gvr, c.shardName, cluster, namespace, name)
			objs, err := c.replicated.Global.GetIndexer().ByIndex(ByGVRAndShardAndLogicalClusterAndNamespaceAndName, key)
			if err != nil {
				return nil, err // necessary to avoid non-zero nil interface
			}

			if len(objs) == 0 {
				return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
			} else if len(objs) > 1 {
				return nil, fmt.Errorf("found multiple objects for %v|%v/%v", cluster, namespace, name)
			}

			return objs[0].(*cachev1alpha1.CachedObject), nil
		},
		getGlobalCopyFromCachedObject: func(cachedObj *cachev1alpha1.CachedObject) (*unstructured.Unstructured, error) {
			gvr := gvrFromKey

			u, err := toUnstructured(&cachedObj.Spec.Raw)
			if err != nil {
				return nil, err
			}
			u = u.DeepCopy()
			u.SetKind(c.replicated.Kind)
			u.SetAPIVersion(gvr.GroupVersion().String())
			return u, nil
		},
		getLocalCopy: func(ctx context.Context, cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
			gvr := gvrFromKey

			obj, err := c.dynamicClusterClient.Cluster(cluster.Path()).
				Resource(gvrFromKey).
				Namespace(namespace).
				Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}

			obj.SetKind(c.replicated.Kind)
			obj.SetAPIVersion(gvr.GroupVersion().String())

			// Append system annotations to the object.
			annotations := obj.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations[genericrequest.ShardAnnotationKey] = c.shardName
			annotations[AnnotationKeyOriginalResourceUID] = string(obj.GetUID())
			annotations[AnnotationKeyOriginalResourceVersion] = obj.GetResourceVersion()
			obj.SetAnnotations(annotations)

			return obj, nil
		},
		createObject: func(ctx context.Context, cluster logicalcluster.Name, local *unstructured.Unstructured) (*cachev1alpha1.CachedObject, error) {
			gvr := gvrFromKey
			if gvr.Group == "" {
				gvr.Group = "core"
			}

			objBytes, err := json.Marshal(local)
			if err != nil {
				return nil, err
			}
			cacheObj := &cachev1alpha1.CachedObject{
				TypeMeta: metav1.TypeMeta{
					Kind:       cache.CachedObjectKind,
					APIVersion: cachev1alpha1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:              GenCachedObjectName(gvr, local.GetNamespace(), local.GetName()),
					Labels:            local.GetLabels(),
					Annotations:       local.GetAnnotations(),
					CreationTimestamp: metav1.NewTime(time.Now()),
				},
				Spec: cachev1alpha1.CachedObjectSpec{
					Raw: runtime.RawExtension{Raw: objBytes},
				},
			}
			if cacheObj.Labels == nil {
				cacheObj.Labels = map[string]string{}
			}
			// Append schema label to the object.
			cacheObj.Labels[LabelKeyObjectSchema] = gvr.Version + "." + gvr.Resource + "." + gvr.Group
			cacheObj.Labels[LabelKeyObjectGroup] = gvr.Group
			cacheObj.Labels[LabelKeyObjectVersion] = gvr.Version
			cacheObj.Labels[LabelKeyObjectResource] = gvr.Resource
			cacheObj.Labels[LabelKeyObjectOriginalName] = local.GetName()
			cacheObj.Labels[LabelKeyObjectOriginalNamespace] = local.GetNamespace()

			u, err := c.kcpCacheClient.Cluster(cluster.Path()).CacheV1alpha1().CachedObjects().Create(ctx, cacheObj, metav1.CreateOptions{})
			return u, err
		},
		updateCachedObjectWithLocalUnstructured: func(ctx context.Context, cluster logicalcluster.Name, origCachedObj *cachev1alpha1.CachedObject, local *unstructured.Unstructured) (*cachev1alpha1.CachedObject, error) {
			gvr := gvrFromKey
			if gvr.Group == "" {
				gvr.Group = "core"
			}

			objBytes, err := json.Marshal(local)
			if err != nil {
				return nil, err
			}

			cacheObj := &cachev1alpha1.CachedObject{
				TypeMeta: metav1.TypeMeta{
					Kind:       cache.CachedObjectKind,
					APIVersion: cachev1alpha1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:            GenCachedObjectName(gvr, local.GetNamespace(), local.GetName()),
					Labels:          origCachedObj.GetLabels(),
					Annotations:     origCachedObj.GetAnnotations(),
					ResourceVersion: origCachedObj.GetResourceVersion(),
				},
				Spec: cachev1alpha1.CachedObjectSpec{
					Raw: runtime.RawExtension{Raw: objBytes},
				},
			}
			if cacheObj.Labels == nil {
				cacheObj.Labels = map[string]string{}
			}
			// Append schema label to the object.
			cacheObj.Labels[LabelKeyObjectSchema] = gvr.Version + "." + gvr.Resource + "." + gvr.Group
			cacheObj.Labels[LabelKeyObjectGroup] = gvr.Group
			cacheObj.Labels[LabelKeyObjectVersion] = gvr.Version
			cacheObj.Labels[LabelKeyObjectResource] = gvr.Resource
			cacheObj.Labels[LabelKeyObjectOriginalName] = local.GetName()
			cacheObj.Labels[LabelKeyObjectOriginalNamespace] = local.GetNamespace()

			return c.kcpCacheClient.Cluster(cluster.Path()).CacheV1alpha1().CachedObjects().Update(ctx, cacheObj, metav1.UpdateOptions{})
		},
		deleteObject: func(ctx context.Context, cluster logicalcluster.Name, ns, name string) error {
			// deleting from cache - means we delete the wrapper object

			gvr := gvrFromKey
			if gvr.Group == "" {
				gvr.Group = "core"
			}

			cachedObjName := GenCachedObjectName(gvr, ns, name)
			if ns != "" {
				cachedObjName += "." + ns
			}
			return c.kcpCacheClient.Cluster(cluster.Path()).CacheV1alpha1().CachedObjects().Delete(ctx, cachedObjName, metav1.DeleteOptions{})
		},
	}
	defer c.callback()
	return r.reconcile(ctx, key)
}

type replicationReconciler struct {
	shardName          string
	deleted            bool
	localLabelSelector labels.Selector

	getLocalPartialObjectMetadata func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error)
	getCachedObject               func(ctx context.Context, cluster logicalcluster.Name, namespace, name string) (*cachev1alpha1.CachedObject, error)
	getGlobalCopyFromCachedObject func(cachedObj *cachev1alpha1.CachedObject) (*unstructured.Unstructured, error)
	getLocalCopy                  func(ctx context.Context, cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error)

	createObject                            func(ctx context.Context, cluster logicalcluster.Name, local *unstructured.Unstructured) (*cachev1alpha1.CachedObject, error)
	updateCachedObjectWithLocalUnstructured func(ctx context.Context, cluster logicalcluster.Name, cachedObj *cachev1alpha1.CachedObject, local *unstructured.Unstructured) (*cachev1alpha1.CachedObject, error)
	deleteObject                            func(ctx context.Context, cluster logicalcluster.Name, ns, name string) error
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

	localPartialObjMeta, err := r.getLocalPartialObjectMetadata(clusterName, ns, name)
	if err != nil && !apierrors.IsNotFound(err) {
		utilruntime.HandleError(err)
		return nil
	}
	localExists := !apierrors.IsNotFound(err)

	// we only replicate objects that match the label selector.
	if localExists && r.localLabelSelector != nil && !r.localLabelSelector.Matches(labels.Set(localPartialObjMeta.GetLabels())) {
		logger.V(2).WithValues("cluster", clusterName, "namespace", ns, "name", name).Info("Object does not match label selector, skipping")
		return nil
	}

	cachedObj, err := r.getCachedObject(ctx, clusterName, ns, name)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	cachedObjExists := !apierrors.IsNotFound(err)

	// local is gone or being deleted. Delete in cache.
	if !localExists || !localPartialObjMeta.GetDeletionTimestamp().IsZero() {
		if !cachedObjExists {
			return nil
		}

		// Object doesn't exist anymore, delete it from the global cache.
		logger.V(2).WithValues("cluster", clusterName, "namespace", ns, "name", name).Info("Deleting object from global cache")
		if err := r.deleteObject(ctx, clusterName, ns, name); err != nil && !apierrors.IsNotFound(err) {
			return err
		}

		return nil
	}

	var globalCopy *unstructured.Unstructured
	if cachedObjExists {
		globalCopy, err = r.getGlobalCopyFromCachedObject(cachedObj)
		if err != nil {
			return err
		}
		// Exit early if there were no changes on the resource.
		if localPartialObjMeta.GetResourceVersion() != "" && globalCopy.GetResourceVersion() == localPartialObjMeta.GetResourceVersion() {
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

	if !cachedObjExists {
		logger.V(2).Info("Creating object in global cache")
		_, err = r.createObject(ctx, clusterName, localCopy)
		return err
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

	logger.V(2).WithValues("kind", globalCopy.GetKind(), "namespace", globalCopy.GetNamespace(), "name", globalCopy.GetName()).Info("Updating object in global cache")
	_, err = r.updateCachedObjectWithLocalUnstructured(ctx, clusterName, cachedObj, localCopy) // no need for patch because there is only this actor
	return err
}
