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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

	"github.com/kcp-dev/kcp/sdk/apis/cache"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
)

const (
	LabelKeyObjectSchema            = "cache.kcp.io/object-schema"
	LabelKeyObjectGroup             = "cache.kcp.io/object-group"
	LabelKeyObjectVersion           = "cache.kcp.io/object-version"
	LabelKeyObjectResource          = "cache.kcp.io/object-resource"
	LabelKeyObjectOriginalName      = "cache.kcp.io/object-original-name"
	LabelKeyObjectOriginalNamespace = "cache.kcp.io/object-original-namespace"
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
	gvr := schema.GroupVersionResource{Version: gvrParts[0], Resource: gvrParts[1], Group: gvrParts[2]}

	// Key will present in the form of namespace/name in the current logical cluster.
	key := keyParts[1]

	r := &replicationReconciler{
		shardName:          c.shardName,
		localLabelSelector: c.localLabelSelector,
		getLocalCopy: func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
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
		getGlobalCopy: func(cluster logicalcluster.Name, namespace, name string, gvr schema.GroupVersionResource) (*unstructured.Unstructured, error) {
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

			obj := objs[0]

			u, err := toUnstructured(obj)
			if err != nil {
				return nil, err
			}
			if _, ok := obj.(*unstructured.Unstructured); ok {
				u = u.DeepCopy()
			}

			u.SetKind(c.replicated.Kind)
			u.SetAPIVersion(gvr.GroupVersion().String())

			return u, nil
		},
		createObject: func(ctx context.Context, cluster logicalcluster.Name, obj *unstructured.Unstructured) (*cachev1alpha1.CachedObject, error) {
			gvk := obj.GroupVersionKind()
			if gvk.Group == "" {
				gvk.Group = "core"
			}

			objBytes, err := json.Marshal(obj)
			if err != nil {
				return nil, err
			}
			mapper, err := c.dynRESTMapper.ForCluster(cluster).RESTMapping(gvk.GroupKind(), gvk.Version)
			if err != nil {
				return nil, err
			}
			cacheObj := &cachev1alpha1.CachedObject{
				TypeMeta: metav1.TypeMeta{
					Kind:       cache.CachedObjectKind,
					APIVersion: cachev1alpha1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:              gvr.Version + "." + mapper.Resource.Resource + "." + gvr.Group + "." + obj.GetName(), // TODO: handle namespace
					Labels:            obj.GetLabels(),
					Annotations:       obj.GetAnnotations(),
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
			cacheObj.Labels[LabelKeyObjectSchema] = gvr.Version + "." + mapper.Resource.Resource + "." + gvr.Group
			cacheObj.Labels[LabelKeyObjectGroup] = gvr.Group
			cacheObj.Labels[LabelKeyObjectVersion] = gvr.Version
			cacheObj.Labels[LabelKeyObjectResource] = mapper.Resource.Resource
			cacheObj.Labels[LabelKeyObjectOriginalName] = obj.GetName()
			cacheObj.Labels[LabelKeyObjectOriginalNamespace] = obj.GetNamespace()

			u, err := c.kcpCacheClient.Cluster(cluster.Path()).CacheV1alpha1().CachedObjects().Create(ctx, cacheObj, metav1.CreateOptions{})
			return u, err
		},
		updateObject: func(ctx context.Context, cluster logicalcluster.Name, obj *unstructured.Unstructured) (*cachev1alpha1.CachedObject, error) {
			gvk := obj.GroupVersionKind()
			if gvk.Group == "" {
				gvk.Group = "core"
			}

			objBytes, err := json.Marshal(obj)
			if err != nil {
				return nil, err
			}

			mapper, err := c.dynRESTMapper.ForCluster(cluster).RESTMapping(gvk.GroupKind(), gvk.Version)
			if err != nil {
				return nil, err
			}
			cacheObj := &cachev1alpha1.CachedObject{
				TypeMeta: metav1.TypeMeta{
					Kind:       cache.CachedObjectKind,
					APIVersion: cachev1alpha1.SchemeGroupVersion.String(),
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:            gvr.Version + "." + mapper.Resource.Resource + "." + gvr.Group + "." + obj.GetName(),
					Labels:          obj.GetLabels(),
					Annotations:     obj.GetAnnotations(),
					ResourceVersion: obj.GetResourceVersion(),
				},
				Spec: cachev1alpha1.CachedObjectSpec{
					Raw: runtime.RawExtension{Raw: objBytes},
				},
			}
			if cacheObj.Labels == nil {
				cacheObj.Labels = map[string]string{}
			}
			// Append schema label to the object.
			cacheObj.Labels[LabelKeyObjectSchema] = gvr.Version + "." + mapper.Resource.Resource + "." + gvr.Group
			cacheObj.Labels[LabelKeyObjectGroup] = gvr.Group
			cacheObj.Labels[LabelKeyObjectVersion] = gvr.Version
			cacheObj.Labels[LabelKeyObjectResource] = mapper.Resource.Resource
			cacheObj.Labels[LabelKeyObjectOriginalName] = obj.GetName()
			cacheObj.Labels[LabelKeyObjectOriginalNamespace] = obj.GetNamespace()

			return c.kcpCacheClient.Cluster(cluster.Path()).CacheV1alpha1().CachedObjects().Update(ctx, cacheObj, metav1.UpdateOptions{})
		},
		deleteObject: func(ctx context.Context, cluster logicalcluster.Name, ns, name string, gvr schema.GroupVersionResource) error {
			// deleting from cache - means we delete the wrapper object
			nameCache := gvr.Version + "." + gvr.Resource + "." + gvr.Group + "." + name // TODO: handle namespace
			return c.kcpCacheClient.Cluster(cluster.Path()).CacheV1alpha1().CachedObjects().Delete(ctx, nameCache, metav1.DeleteOptions{})
		},
	}
	defer c.callback()
	return r.reconcile(ctx, gvr, key)
}

type replicationReconciler struct {
	shardName          string
	deleted            bool
	localLabelSelector labels.Selector

	getLocalCopy  func(cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error)
	getGlobalCopy func(cluster logicalcluster.Name, namespace, name string, gvr schema.GroupVersionResource) (*unstructured.Unstructured, error)

	createObject func(ctx context.Context, cluster logicalcluster.Name, obj *unstructured.Unstructured) (*cachev1alpha1.CachedObject, error)
	updateObject func(ctx context.Context, cluster logicalcluster.Name, obj *unstructured.Unstructured) (*cachev1alpha1.CachedObject, error)
	deleteObject func(ctx context.Context, cluster logicalcluster.Name, ns, name string, gvr schema.GroupVersionResource) error
}

// reconcile makes sure that the object under the given key from the local shard is replicated to the cache server.
// the replication function handles the following cases:
//  1. creation of the object in the cache server when the cached object is not found by getGlobalCopy
//  2. deletion of the object from the cache server when the original/local object was removed OR was not found by getLocalCopy
//  3. modification of the cached object to match the original one when meta.annotations, meta.labels, spec or status are different
func (r *replicationReconciler) reconcile(ctx context.Context, gvr schema.GroupVersionResource, key string) error {
	if r.deleted {
		return nil
	}
	logger := klog.FromContext(ctx).WithValues("reconcilerKey", key)

	clusterName, ns, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return nil
	}

	localCopy, err := r.getLocalCopy(clusterName, ns, name)
	if err != nil && !apierrors.IsNotFound(err) {
		utilruntime.HandleError(err)
		return nil
	}
	localExists := !apierrors.IsNotFound(err)

	// we only replicate objects that match the label selector.
	if localExists && r.localLabelSelector != nil && !r.localLabelSelector.Matches(labels.Set(localCopy.GetLabels())) {
		logger.V(2).WithValues("cluster", clusterName, "namespace", ns, "name", name).Info("Object does not match label selector, skipping")
		return nil
	}

	globalCopy, err := r.getGlobalCopy(clusterName, ns, name, gvr)
	if err != nil && !apierrors.IsNotFound(err) {
		utilruntime.HandleError(err)
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
		if err := r.deleteObject(ctx, clusterName, ns, name, gvr); err != nil && !apierrors.IsNotFound(err) {
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
	_, err = r.updateObject(ctx, clusterName, globalCopy) // no need for patch because there is only this actor
	return err
}
