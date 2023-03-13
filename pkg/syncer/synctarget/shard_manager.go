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

package synctarget

import (
	"context"
	"fmt"
	"sync"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/informer"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
)

// NewLogicalClusterIndex creates an index that contains all the keys of the synced and upsynced resources,
// indexed by logical cluster name.
// This index is filled by the syncer and upsyncer ddsifs, and is used to check if the related shard contains
// a given logicalCluster.
func NewLogicalClusterIndex(syncerDDSIF, upsyncerDDSIF *informer.DiscoveringDynamicSharedInformerFactory) *logicalClusterIndex {
	index := &logicalClusterIndex{
		indexedKeys:     map[string]map[string]interface{}{},
		indexedKeysLock: sync.RWMutex{},
	}

	clusterNameAndKey := func(syncerSource string, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) (clusterName, clusterKeys string, err error) {
		clusterName = logicalcluster.From(obj).String()
		key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
		if err != nil {
			return "", "", err
		}

		return clusterName, fmt.Sprintf("%s$$%s.%s.%s##%s", syncerSource, gvr.Resource, gvr.Group, gvr.Version, key), nil
	}

	add := func(syncerSource string, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
		clusterName, clusterKey, err := clusterNameAndKey(syncerSource, gvr, obj)
		if err != nil {
			return err
		}

		index.indexedKeysLock.Lock()
		defer index.indexedKeysLock.Unlock()

		clusterKeys, ok := index.indexedKeys[clusterName]
		if !ok {
			clusterKeys = map[string]interface{}{}
			index.indexedKeys[clusterName] = clusterKeys
		}
		clusterKeys[clusterKey] = nil
		return nil
	}

	delete := func(syncerSource string, gvr schema.GroupVersionResource, obj *unstructured.Unstructured) error {
		clusterName, clusterKey, err := clusterNameAndKey(syncerSource, gvr, obj)
		if err != nil {
			return err
		}

		index.indexedKeysLock.Lock()
		defer index.indexedKeysLock.Unlock()

		clusterKeys, ok := index.indexedKeys[clusterName]
		if !ok {
			return nil
		}
		delete(clusterKeys, clusterKey)
		if len(clusterKeys) == 0 {
			delete(index.indexedKeys, clusterName)
		}
		return nil
	}

	syncerDDSIF.AddEventHandler(informer.GVREventHandlerFuncs{
		AddFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			_ = add("S", gvr, obj.(*unstructured.Unstructured))
		},
		DeleteFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			_ = add("S", gvr, obj.(*unstructured.Unstructured))
		},
	})

	upsyncerDDSIF.AddEventHandler(informer.GVREventHandlerFuncs{
		AddFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			_ = delete("U", gvr, obj.(*unstructured.Unstructured))
		},
		DeleteFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
			_ = delete("U", gvr, obj.(*unstructured.Unstructured))
		},
	})

	return index
}

// Exists returns true if the logicalClusterIndex contains at least one value
// for the given clusterName.
func (index *logicalClusterIndex) Exists(clusterName logicalcluster.Name) bool {
	index.indexedKeysLock.Lock()
	defer index.indexedKeysLock.Unlock()

	_, ok := index.indexedKeys[string(clusterName)]
	return ok
}

type logicalClusterIndex struct {
	indexedKeys     map[string]map[string]interface{}
	indexedKeysLock sync.RWMutex
}

// ShardAccess contains clustered dynamic clients, as well as
// cluster-aware informer factories for both the Syncer and Upsyncer virtual workspaces
// associated to a Shard.
type ShardAccess struct {
	SyncerClient   kcpdynamic.ClusterInterface
	SyncerDDSIF    *informer.DiscoveringDynamicSharedInformerFactory
	UpsyncerClient kcpdynamic.ClusterInterface
	UpsyncerDDSIF  *informer.DiscoveringDynamicSharedInformerFactory

	// LogicalClusterIndex contains all the keys of the synced and upsynced resources
	// indexed by logical cluster name.
	LogicalClusterIndex *logicalClusterIndex
}

// GetShardAccessFunc is the type of a function that provide a [ShardAccess] from a
// logical cluster name.
type GetShardAccessFunc func(clusterName logicalcluster.Name) (ShardAccess, bool, error)

// NewShardManager returns a [ShardManager] that can manage the addition or removal of shard-specific
// upstream virtual workspace URLs, based on a SyncTarget resource passed to the updateShards() method.
//
// When a shard is found (identified by the couple of virtual workspace URLs - for both syncer and upsyncer),
// then the startShardControllers() method is called, and the resulting [ShardAccess] is stored.
//
// When a shard is removed, the cleanupShard() method is called, and the context initially passed to the
// startShardControllers() method is cancelled.
//
// The ShardAccessForCluster() method will be used by some downstream controllers in order to
// be able to get / list upstream resources in the right shard.
func NewShardManager(
	startShardControllers func(ctx context.Context, shardURLs workloadv1alpha1.VirtualWorkspace) (*ShardAccess, error),
	cleanupShard func(urls workloadv1alpha1.VirtualWorkspace)) *shardManager {
	return &shardManager{
		controllers:           map[workloadv1alpha1.VirtualWorkspace]shardControllers{},
		startShardControllers: startShardControllers,
		cleanupShard:          cleanupShard,
	}
}

type shardManager struct {
	controllersLock       sync.RWMutex
	controllers           map[workloadv1alpha1.VirtualWorkspace]shardControllers
	startShardControllers func(ctx context.Context, shardURLs workloadv1alpha1.VirtualWorkspace) (*ShardAccess, error)
	cleanupShard          func(urls workloadv1alpha1.VirtualWorkspace)
}

func (c *shardManager) ShardAccessForCluster(clusterName logicalcluster.Name) (ShardAccess, bool, error) {
	c.controllersLock.RLock()
	defer c.controllersLock.RUnlock()

	for _, shardControllers := range c.controllers {
		if shardControllers.ShardAccess.LogicalClusterIndex.Exists(clusterName) {
			return shardControllers.ShardAccess, true, nil
		}
	}
	return ShardAccess{}, false, nil
}

func (c *shardManager) reconcile(ctx context.Context, syncTarget *workloadv1alpha1.SyncTarget) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)

	requiredShards := map[workloadv1alpha1.VirtualWorkspace]bool{}
	if syncTarget != nil {
		for _, shardURLs := range syncTarget.Status.VirtualWorkspaces {
			requiredShards[shardURLs] = true
		}
	}

	c.controllersLock.Lock()
	defer c.controllersLock.Unlock()

	// Remove obsolete controllers that don't have a shard anymore
	for shardURLs, shardControllers := range c.controllers {
		if _, ok := requiredShards[shardURLs]; ok {
			// The controllers are still expected => don't remove them
			continue
		}
		// The controllers should not be running
		// Stop them and remove it from the list of started shard controllers
		shardControllers.stop()
		delete(c.controllers, shardURLs)
	}

	var errs []error
	// Create and start missing controllers that have Virtual Workspace URLs for a shard
	for shardURLs := range requiredShards {
		if _, ok := c.controllers[shardURLs]; ok {
			// The controllers are already started
			continue
		}

		// Start the controllers
		shardControllersContext, cancelFunc := context.WithCancel(ctx)
		stop := func() {
			c.cleanupShard(shardURLs)
			cancelFunc()
		}
		// Create the controllers
		shardAccess, err := c.startShardControllers(shardControllersContext, shardURLs)
		if err != nil {
			logger.Error(err, "failed creating controllers for shard", "shard", shardURLs)
			errs = append(errs, err)
			stop()
			continue
		}
		c.controllers[shardURLs] = shardControllers{
			*shardAccess, cancelFunc,
		}
	}
	return reconcileStatusContinue, utilserrors.NewAggregate(errs)
}

type shardControllers struct {
	ShardAccess
	stop func()
}
