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

	"github.com/google/go-cmp/cmp"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	genericrequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"
)

// ensureMeta changes unstructuredCacheObject's metadata to match unstructuredLocalObject's metadata except the ResourceVersion and the shard annotation fields.
func ensureMeta(cacheObject *unstructured.Unstructured, localObject *unstructured.Unstructured) (changed bool, reason string, err error) {
	cacheObjMetaRaw, hasCacheObjMetaRaw, err := unstructured.NestedFieldNoCopy(cacheObject.Object, "metadata")
	if err != nil {
		return false, "", err
	}
	cacheObjMeta, ok := cacheObjMetaRaw.(map[string]interface{})
	if !ok {
		return false, "", fmt.Errorf("metadata field of unstructuredCacheObject is of the type %T, expected map[string]interface{}", cacheObjMetaRaw)
	}
	localObjMetaRaw, hasLocalObjMetaRaw, err := unstructured.NestedFieldNoCopy(localObject.Object, "metadata")
	if err != nil {
		return false, "", err
	}
	localObjMeta, ok := localObjMetaRaw.(map[string]interface{})
	if !ok {
		return false, "", fmt.Errorf("metadata field of unstructuredLocalObjectMeta is of the type %T, expected map[string]interface{}", localObjMetaRaw)
	}
	if !hasLocalObjMetaRaw && !hasCacheObjMetaRaw {
		return false, "", nil // no-op
	}
	if !hasLocalObjMetaRaw {
		unstructured.RemoveNestedField(cacheObject.Object, "metadata")
		return true, "local .metadata disappeared", nil
	}

	// before we can compare the local and cache objects we need to remove certain
	// field we know will be different, and bring them back after the comparison.
	for _, pth := range []string{"resourceVersion", "generation", "managedFields"} {
		pth := pth
		if v, found := cacheObjMeta[pth]; found {
			delete(cacheObjMeta, pth)
			defer func() { //nolint:gocritic
				if err == nil {
					err = unstructured.SetNestedField(cacheObject.Object, v, "metadata", pth)
				}
			}()
		}
		if v, found := localObjMeta[pth]; found {
			delete(localObjMeta, pth)
			defer func() { //nolint:gocritic
				if err == nil {
					err = unstructured.SetNestedField(localObject.Object, v, "metadata", pth)
				}
			}()
		}
	}
	if cacheObjAnnotationsRaw, found := cacheObjMeta["annotations"]; found {
		cacheObjAnnotations, ok := cacheObjAnnotationsRaw.(map[string]interface{})
		if !ok {
			return false, "", fmt.Errorf("metadata.annotations field of unstructuredCacheObject is of the type %T, expected map[string]interface{}", cacheObjAnnotationsRaw)
		}
		if shard, hasShard := cacheObjAnnotations[genericrequest.ShardAnnotationKey]; hasShard {
			delete(cacheObjAnnotations, genericrequest.ShardAnnotationKey)
			defer func() {
				if err == nil {
					err = unstructured.SetNestedField(cacheObject.Object, shard, "metadata", "annotations", genericrequest.ShardAnnotationKey)
				}
			}()
		}
		// TODO: in the future the original RV will be stored in an annotation
	}

	changed = !reflect.DeepEqual(cacheObjMeta, localObjMeta)
	if !changed {
		return false, "", nil
	}

	reason = ".metadata changed"
	if logger := klog.FromContext(context.Background()).V(5); logger.Enabled() {
		reason += " " + cmp.Diff(localObjMeta, cacheObjMeta)
	}

	newCacheObjMeta := map[string]interface{}{}
	for k, v := range localObjMeta {
		newCacheObjMeta[k] = v
	}
	return true, reason, unstructured.SetNestedMap(cacheObject.Object, newCacheObjMeta, "metadata")
}

// ensureRemaining changes unstructuredCacheObject to match unstructuredLocalObject except for the metadata field
// returns true when the unstructuredCacheObject was updated.
func ensureRemaining(cacheObject *unstructured.Unstructured, localObject *unstructured.Unstructured) (bool, string, error) {
	cacheObjMeta, found, err := unstructured.NestedFieldNoCopy(cacheObject.Object, "metadata")
	if err != nil {
		return false, "", err
	}
	if found {
		unstructured.RemoveNestedField(cacheObject.Object, "metadata")
		defer func() {
			cacheObject.Object["metadata"] = cacheObjMeta
		}()
	}

	localObjMeta, found, err := unstructured.NestedFieldNoCopy(localObject.Object, "metadata")
	if err != nil {
		return false, "", err
	}
	if found {
		unstructured.RemoveNestedField(localObject.Object, "metadata")
		defer func() {
			localObject.Object["metadata"] = localObjMeta
		}()
	}

	changed := !reflect.DeepEqual(cacheObject.Object, localObject.Object)
	if !changed {
		return false, "", nil
	}

	reason := "object changed"
	if logger := klog.FromContext(context.Background()).V(5); logger.Enabled() {
		reason += " " + cmp.Diff(localObject.Object, cacheObject.Object)
	}

	newCacheObj := map[string]interface{}{}
	for k, v := range localObject.Object {
		newCacheObj[k] = v
	}
	cacheObject.Object = newCacheObj
	return true, reason, nil
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
