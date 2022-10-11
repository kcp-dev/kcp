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

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func (c *controller) reconcile(ctx context.Context, grKey string) error {
	keyParts := strings.Split(grKey, "::")
	if len(keyParts) != 2 {
		return fmt.Errorf("incorrect key: %v, expected group.resource::key", grKey)
	}
	switch keyParts[0] {
	case apisv1alpha1.SchemeGroupVersion.WithResource("apiexports").String():
		return c.reconcileAPIExports(ctx, keyParts[1], apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"))
	default:
		return fmt.Errorf("unsupported resource %v", keyParts[0])
	}
}

// reconcileAPIExports makes sure that the ApiExport under the given key from the local shard is replicated to the cache server.
// the replication function handles the following cases:
//  1. creation of the object in the cache server when the cached object is not found in c.localApiExportLister
//  2. deletion of the object from the cache server when the original/local object was removed OR was not found in c.localApiExportLister
//  3. modification of the cached object to match the original one when meta.annotations, meta.labels, spec or status are different
func (c *controller) reconcileAPIExports(ctx context.Context, key string, gvr schema.GroupVersionResource) error {
	var cacheApiExport *apisv1alpha1.APIExport
	var localApiExport *apisv1alpha1.APIExport
	cluster, _, apiExportName, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return err
	}
	cacheApiExport, err = c.getCachedAPIExport(c.shardName, cluster, apiExportName)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	localApiExport, err = c.getLocalAPIExport(cluster, apiExportName)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if errors.IsNotFound(err) {
		// issue a live GET to make sure the localApiExport was removed
		unstructuredLiveLocalApiExport, err := c.getLocalObject(ctx, gvr, cluster, "", apiExportName)
		if err == nil {
			return fmt.Errorf("the informer used by this controller is stale, the following APIExport was found on the local server: %s/%s/%s but was missing in the informer", cluster, unstructuredLiveLocalApiExport.GetNamespace(), unstructuredLiveLocalApiExport.GetName())
		}
		if !errors.IsNotFound(err) {
			return err
		}
	}

	var unstructuredCacheApiExport *unstructured.Unstructured
	var unstructuredLocalApiExport *unstructured.Unstructured
	if cacheApiExport != nil {
		unstructuredCacheApiExport, err = toUnstructured(cacheApiExport)
		if err != nil {
			return err
		}
		unstructuredCacheApiExport.SetKind("APIExport")
		unstructuredCacheApiExport.SetAPIVersion(gvr.GroupVersion().String())
	}
	if localApiExport != nil {
		unstructuredLocalApiExport, err = toUnstructured(localApiExport)
		if err != nil {
			return err
		}
		unstructuredLocalApiExport.SetKind("APIExport")
		unstructuredLocalApiExport.SetAPIVersion(gvr.GroupVersion().String())
	}
	if cluster.Empty() && localApiExport != nil {
		cluster = logicalcluster.From(localApiExport)
	}

	return c.reconcileUnstructuredObjects(ctx, cluster, &gvr, unstructuredCacheApiExport, unstructuredLocalApiExport)
}
