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
	"strings"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func (c *controller) reconcile(ctx context.Context, gvrKey string) error {
	keyParts := strings.Split(gvrKey, "::")
	if len(keyParts) != 2 {
		return fmt.Errorf("incorrect key: %v, expected group.version.resource::key", gvrKey)
	}
	switch keyParts[0] {
	case apisv1alpha1.SchemeGroupVersion.WithResource("apiexports").String():
		return c.reconcileObject(ctx,
			keyParts[1],
			apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"),
			apisv1alpha1.SchemeGroupVersion.WithKind("APIExport"),
			func(gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) (interface{}, error) {
				return retrieveCacheObject(&gvr, c.globalAPIExportIndexer, c.shardName, cluster, namespace, name)
			},
			func(cluster logicalcluster.Name, _, name string) (interface{}, error) {
				return c.localAPIExportLister.Cluster(cluster).Get(name)
			})
	case apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas").String():
		return c.reconcileObject(ctx,
			keyParts[1],
			apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas"),
			apisv1alpha1.SchemeGroupVersion.WithKind("APIResourceSchema"),
			func(gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) (interface{}, error) {
				return retrieveCacheObject(&gvr, c.globalAPIResourceSchemaIndexer, c.shardName, cluster, namespace, name)
			},
			func(cluster logicalcluster.Name, _, name string) (interface{}, error) {
				return c.localAPIResourceSchemaLister.Cluster(cluster).Get(name)
			})
	case tenancyv1alpha1.SchemeGroupVersion.WithResource("shards").String():
		return c.reconcileObject(ctx,
			keyParts[1],
			tenancyv1alpha1.SchemeGroupVersion.WithResource("shards"),
			tenancyv1alpha1.SchemeGroupVersion.WithKind("Shard"),
			func(gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) (interface{}, error) {
				return retrieveCacheObject(&gvr, c.globalShardIndexer, c.shardName, cluster, namespace, name)
			},
			func(cluster logicalcluster.Name, _, name string) (interface{}, error) {
				return c.localShardLister.Cluster(cluster).Get(name)
			})
	default:
		return fmt.Errorf("unsupported resource %v", keyParts[0])
	}
}

// reconcileObject makes sure that the object under the given key from the local shard is replicated to the cache server.
// the replication function handles the following cases:
//  1. creation of the object in the cache server when the cached object is not found by retriveLocalObject
//  2. deletion of the object from the cache server when the original/local object was removed OR was not found by retriveLocalObject
//  3. modification of the cached object to match the original one when meta.annotations, meta.labels, spec or status are different
func (c *controller) reconcileObject(ctx context.Context,
	key string, gvr schema.GroupVersionResource, gvk schema.GroupVersionKind,
	retriveCacheObject func(gvr schema.GroupVersionResource, cluster logicalcluster.Name, namespace, name string) (interface{}, error),
	retriveLocalObject func(cluster logicalcluster.Name, namespace, name string) (interface{}, error)) error {
	cluster, namespace, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return err
	}
	cacheObject, err := retriveCacheObject(gvr, cluster, namespace, name)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	localObject, err := retriveLocalObject(cluster, namespace, name)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if errors.IsNotFound(err) {
		// issue a live GET to make sure the localObject was removed
		_, err = c.dynamicLocalClient.Cluster(cluster.Path()).Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			return fmt.Errorf("the informer used by this controller is stale, the following %s resource was found on the local server: %s/%s/%s but was missing from the informer", gvr, cluster, namespace, name)
		}
		if !errors.IsNotFound(err) {
			return err
		}
	}

	var unstructuredCacheObject *unstructured.Unstructured
	var unstructuredLocalObject *unstructured.Unstructured
	if isNotNil(cacheObject) {
		unstructuredCacheObject, err = toUnstructured(cacheObject)
		if err != nil {
			return err
		}
		unstructuredCacheObject.SetKind(gvk.Kind)
		unstructuredCacheObject.SetAPIVersion(gvr.GroupVersion().String())
	}
	if isNotNil(localObject) {
		unstructuredLocalObject, err = toUnstructured(localObject)
		if err != nil {
			return err
		}
		unstructuredLocalObject.SetKind(gvk.Kind)
		unstructuredLocalObject.SetAPIVersion(gvr.GroupVersion().String())
	}
	if cluster.Empty() && isNotNil(localObject) {
		metadata, err := meta.Accessor(localObject)
		if err != nil {
			return err
		}
		cluster = logicalcluster.From(metadata)
	}

	return c.reconcileUnstructuredObjects(ctx, cluster, &gvr, unstructuredCacheObject, unstructuredLocalObject)
}

func retrieveCacheObject(gvr *schema.GroupVersionResource, cacheIndex cache.Indexer, shard string, cluster logicalcluster.Name, namespace, name string) (interface{}, error) {
	cacheObjects, err := cacheIndex.ByIndex(ByShardAndLogicalClusterAndNamespaceAndName, ShardAndLogicalClusterAndNamespaceKey(shard, cluster, namespace, name))
	if err != nil {
		return nil, err
	}
	if len(cacheObjects) == 0 {
		return nil, errors.NewNotFound(gvr.GroupResource(), name)
	}
	if len(cacheObjects) > 1 {
		return nil, fmt.Errorf("expected to find only one instance of %s resource for the key %s, found %d", gvr, ShardAndLogicalClusterAndNamespaceKey(shard, cluster, namespace, name), len(cacheObjects))
	}
	return cacheObjects[0], nil
}

func isNotNil(obj interface{}) bool {
	return obj != nil && (reflect.ValueOf(obj).Kind() == reflect.Ptr && !reflect.ValueOf(obj).IsNil())
}
