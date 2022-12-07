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
	"reflect"

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	genericrequest "k8s.io/apiserver/pkg/endpoints/request"
)

// reconcileUnstructuredObjects makes sure that the given cachedObject of the given GVR under the given key from the local shard is replicated to the cache server.
//
// this method handles the following cases:
//  1. creation of the object in the cache server
//     happens when the cacheObject is nil
//  2. deletion of the object from the cache server
//     happens when either of the following is true:
//     - the localObject object is nil
//     - the localObject was deleted
//  3. modification of the object to match the original/local object
//     happens when either of the following is true:
//     - the localObject's metadata doesn't match the cacheObject
//     - the localObject's spec doesn't match the cacheObject
//     - the localObject's status doesn't match the cacheObject
func (c *controller) reconcileUnstructuredObjects(ctx context.Context, cluster logicalcluster.Name, gvr *schema.GroupVersionResource, cacheObject *unstructured.Unstructured, localObject *unstructured.Unstructured) error {
	if localObject == nil {
		return c.handleObjectDeletion(ctx, cluster, gvr, cacheObject)
	}
	if localObject.GetDeletionTimestamp() != nil {
		return c.handleObjectDeletion(ctx, cluster, gvr, cacheObject)
	}

	if cacheObject == nil {
		// TODO: in the future the original RV will have to be stored in an annotation (?)
		// so that the clients that need to modify the original/local object can do it
		localObject.SetResourceVersion("")
		annotations := localObject.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[genericrequest.AnnotationKey] = c.shardName
		localObject.SetAnnotations(annotations)
		_, err := c.dynamicCacheClient.Cluster(cluster.Path()).Resource(*gvr).Namespace(localObject.GetNamespace()).Create(ctx, localObject, metav1.CreateOptions{})
		return err
	}

	metaChanged, err := ensureMeta(cacheObject, localObject)
	if err != nil {
		return err
	}
	remainingChanged, err := ensureRemaining(cacheObject, localObject)
	if err != nil {
		return err
	}
	if !metaChanged && !remainingChanged {
		return nil
	}

	if metaChanged || remainingChanged {
		_, err := c.dynamicCacheClient.Cluster(cluster.Path()).Resource(*gvr).Namespace(cacheObject.GetNamespace()).Update(ctx, cacheObject, metav1.UpdateOptions{})
		return err
	}
	return nil
}

func (c *controller) handleObjectDeletion(ctx context.Context, cluster logicalcluster.Name, gvr *schema.GroupVersionResource, cacheObject *unstructured.Unstructured) error {
	if cacheObject == nil {
		return nil // the cached object already removed
	}
	if cacheObject.GetDeletionTimestamp() == nil {
		return c.dynamicCacheClient.Cluster(cluster.Path()).Resource(*gvr).Namespace(cacheObject.GetNamespace()).Delete(ctx, cacheObject.GetName(), metav1.DeleteOptions{})
	}
	return nil
}

// ensureMeta changes unstructuredCacheObject's metadata to match unstructuredLocalObject's metadata except the ResourceVersion and the shard annotation fields
func ensureMeta(cacheObject *unstructured.Unstructured, localObject *unstructured.Unstructured) (changed bool, err error) {
	cacheObjMetaRaw, hasCacheObjMetaRaw, err := unstructured.NestedFieldNoCopy(cacheObject.Object, "metadata")
	if err != nil {
		return false, err
	}
	cacheObjMeta, ok := cacheObjMetaRaw.(map[string]interface{})
	if !ok {
		return false, fmt.Errorf("metadata field of unstructuredCacheObject is of the type %T, expected map[string]interface{}", cacheObjMetaRaw)
	}
	localObjMetaRaw, hasLocalObjMetaRaw, err := unstructured.NestedFieldNoCopy(localObject.Object, "metadata")
	if err != nil {
		return false, err
	}
	localObjMeta, ok := localObjMetaRaw.(map[string]interface{})
	if !ok {
		return false, fmt.Errorf("metadata field of unstructuredLocalObjectMeta is of the type %T, expected map[string]interface{}", localObjMetaRaw)
	}
	if !hasLocalObjMetaRaw && !hasCacheObjMetaRaw {
		return false, nil // no-op
	}
	if !hasLocalObjMetaRaw {
		unstructured.RemoveNestedField(cacheObject.Object, "metadata")
		return true, nil
	}

	// before we can compare the cache object we need to
	// store, remove and then bring back fields that are unique only to the cache object
	if cacheObjRV, found := cacheObjMeta["resourceVersion"]; found {
		unstructured.RemoveNestedField(cacheObjMeta, "resourceVersion")
		defer func() {
			if err == nil {
				err = unstructured.SetNestedField(cacheObject.Object, cacheObjRV, "metadata", "resourceVersion")
			}
		}()
	}
	if cacheObjAnnotationsRaw, found := cacheObjMeta["annotations"]; found {
		cacheObjAnnotations, ok := cacheObjAnnotationsRaw.(map[string]interface{})
		if !ok {
			return false, fmt.Errorf("metadata.annotations field of unstructuredCacheObject is of the type %T, expected map[string]interface{}", cacheObjAnnotationsRaw)
		}
		if shard, hasShard := cacheObjAnnotations[genericrequest.AnnotationKey]; hasShard {
			unstructured.RemoveNestedField(cacheObjAnnotations, genericrequest.AnnotationKey)
			defer func() {
				if err == nil {
					err = unstructured.SetNestedField(cacheObject.Object, shard, "metadata", "annotations", genericrequest.AnnotationKey)
				}
			}()
		}
		// TODO: in the future the original RV will be stored in an annotation
	}

	// before we can compare with the local object we need to
	// store, remove and then bring back the ResourceVersion on the local object
	if localObjRV, found := localObjMeta["resourceVersion"]; found {
		unstructured.RemoveNestedField(localObjMeta, "resourceVersion")
		defer func() {
			if err == nil {
				localObjMeta["resourceVersion"] = localObjRV
			}
		}()
	}

	changed = !reflect.DeepEqual(cacheObjMeta, localObjMeta)
	if !changed {
		return false, nil
	}

	newCacheObjMeta := map[string]interface{}{}
	for k, v := range localObjMeta {
		newCacheObjMeta[k] = v
	}
	return true, unstructured.SetNestedMap(cacheObject.Object, newCacheObjMeta, "metadata")
}

// ensureRemaining changes unstructuredCacheObject to match unstructuredLocalObject except for the metadata field
// returns true when the unstructuredCacheObject was updated.
func ensureRemaining(cacheObject *unstructured.Unstructured, localObject *unstructured.Unstructured) (bool, error) {
	cacheObjMeta, found, err := unstructured.NestedFieldNoCopy(cacheObject.Object, "metadata")
	if err != nil {
		return false, err
	}
	if found {
		unstructured.RemoveNestedField(cacheObject.Object, "metadata")
		defer func() {
			cacheObject.Object["metadata"] = cacheObjMeta
		}()
	}

	localObjMeta, found, err := unstructured.NestedFieldNoCopy(localObject.Object, "metadata")
	if err != nil {
		return false, err
	}
	if found {
		unstructured.RemoveNestedField(localObject.Object, "metadata")
		defer func() {
			localObject.Object["metadata"] = localObjMeta
		}()
	}

	changed := !reflect.DeepEqual(cacheObject.Object, localObject.Object)
	if !changed {
		return false, nil
	}

	newCacheObj := map[string]interface{}{}
	for k, v := range localObject.Object {
		newCacheObj[k] = v
	}
	cacheObject.Object = newCacheObj
	return true, nil
}

func toUnstructured(obj interface{}) (*unstructured.Unstructured, error) {
	unstructured := &unstructured.Unstructured{Object: map[string]interface{}{}}
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	unstructured.Object = raw
	return unstructured, nil
}
