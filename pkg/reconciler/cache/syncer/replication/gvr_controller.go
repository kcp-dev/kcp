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

package replication

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpdynamicinformer "github.com/kcp-dev/client-go/dynamic/dynamicinformer"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	clientshard "github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const resyncPeriodGVR = 10 * time.Hour

// peerInformerHandle holds the runtime resources for one peer's GVR informer.
type peerInformerHandle struct {
	informer      cache.SharedIndexInformer
	dynamicClient kcpdynamic.ClusterInterface
	cancel        context.CancelFunc
}

// GVRController replicates all objects of one GVR from the authoritative source
// shards to every known peer cache-server. Each peer is one queue item; reconcile
// does a full set-diff for that GVR on that peer.
type GVRController struct {
	gvr           schema.GroupVersionResource
	ownName       string
	peerTLSConfig rest.TLSClientConfig

	// sourceConfig is the pre-wrapped REST config for the source cache-server.
	sourceConfig *rest.Config
	// sourceInformer watches all objects of this GVR on the source. Set in Start.
	sourceInformer cache.SharedIndexInformer

	// cacheInformer is the shared Cache object informer owned by the root controller.
	cacheInformer cache.SharedIndexInformer
	cacheReg      cache.ResourceEventHandlerRegistration

	// shardLister is used during reconcile to determine authoritative shards.
	shardLister corev1alpha1listers.ShardClusterLister

	// peerClients is the shared peer client map owned by the root controller.
	peerClients *PeerClientMap

	// cancelFunc cancels the sub-context created by the root controller.
	cancelFunc context.CancelFunc

	queue workqueue.TypedRateLimitingInterface[string]

	mu            sync.RWMutex
	peerInformers map[string]*peerInformerHandle
}

func newGVRController(
	gvr schema.GroupVersionResource,
	sourceConfig *rest.Config,
	peerTLSConfig rest.TLSClientConfig,
	cacheInformer cache.SharedIndexInformer,
	shardLister corev1alpha1listers.ShardClusterLister,
	peerClients *PeerClientMap,
	ownName string,
	cancelFunc context.CancelFunc,
) *GVRController {
	return &GVRController{
		gvr:           gvr,
		ownName:       ownName,
		peerTLSConfig: peerTLSConfig,
		sourceConfig:  sourceConfig,
		cacheInformer: cacheInformer,
		shardLister:   shardLister,
		peerClients:   peerClients,
		cancelFunc:    cancelFunc,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: fmt.Sprintf("%s/%s", ControllerName, gvr.String()),
			},
		),
		peerInformers: make(map[string]*peerInformerHandle),
	}
}

// Start implements the GVRController lifecycle:
//  1. Register Cache ADD/DEL handler on the shared Cache informer.
//  2. Start peer informers for all currently known peers.
//  3. Start source dynamic informer; wait for HasSynced.
//  4. Launch queue workers.
func (c *GVRController) Start(ctx context.Context) {
	logger := logging.WithReconciler(klog.FromContext(ctx), fmt.Sprintf("%s/%s", ControllerName, c.gvr))
	ctx = klog.NewContext(ctx, logger)
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	// Step 1: Register Cache ADD/DEL handler before listing peers so no events
	// are missed between registration and the initial peer list in step 2.
	reg, err := c.cacheInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.handleCacheAdd(ctx, obj)
		},
		DeleteFunc: func(obj interface{}) {
			c.handleCacheDel(obj)
		},
	})
	if err != nil {
		logger.Error(err, "failed to register Cache event handler")
		return
	}
	c.cacheReg = reg

	// Step 2: Start peer informers for all peers already in peerClients.
	for _, peerName := range c.peerClients.Names() {
		c.startPeerInformer(ctx, peerName)
		c.queue.Add(peerName)
	}

	// Step 3: Create and start the source dynamic informer.
	sourceDynamic, err := kcpdynamic.NewForConfig(c.sourceConfig)
	if err != nil {
		logger.Error(err, "failed to create source dynamic client")
		return
	}
	clusterInformer := kcpdynamicinformer.NewFilteredDynamicInformer(
		sourceDynamic,
		c.gvr,
		resyncPeriodGVR,
		cache.Indexers{
			kcpcache.ClusterIndexName:             kcpcache.ClusterIndexFunc,
			kcpcache.ClusterAndNamespaceIndexName: kcpcache.ClusterAndNamespaceIndexFunc,
		},
		nil,
	)
	c.sourceInformer = clusterInformer.Informer()
	// Enqueue all peers when a source object changes.
	if _, err := c.sourceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(_ interface{}) { c.enqueueAllPeers() },
		UpdateFunc: func(_, _ interface{}) { c.enqueueAllPeers() },
		DeleteFunc: func(_ interface{}) { c.enqueueAllPeers() },
	}); err != nil {
		logger.Error(err, "failed to register source informer event handler")
		return
	}
	go c.sourceInformer.Run(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), c.sourceInformer.HasSynced) {
		if ctx.Err() == nil {
			logger.Error(nil, "timed out waiting for source informer cache sync")
		}
		return
	}

	// Step 4: Launch workers.
	for range 2 {
		go wait.UntilWithContext(ctx, c.runWorker, time.Second)
	}

	logger.Info("GVRController started")
	<-ctx.Done()
	logger.Info("GVRController stopped")

	// Stop all peer informers.
	c.mu.Lock()
	for peerName, handle := range c.peerInformers {
		handle.cancel()
		delete(c.peerInformers, peerName)
	}
	c.mu.Unlock()
}

// Stop deregisters the Cache event handler and cancels the controller's context.
func (c *GVRController) Stop() {
	if c.cacheReg != nil {
		if err := c.cacheInformer.RemoveEventHandler(c.cacheReg); err != nil {
			klog.Errorf("GVRController %v: failed to remove Cache event handler: %v", c.gvr, err)
		}
	}
	c.cancelFunc()
}

// handleCacheAdd reacts to a new Cache object (new peer): adds it to peerClients,
// starts a peer informer, and enqueues for reconcile.
func (c *GVRController) handleCacheAdd(ctx context.Context, obj interface{}) {
	cacheObj, ok := obj.(*corev1alpha1.Cache)
	if !ok {
		return
	}
	if cacheObj.Name == c.ownName || cacheObj.Spec.BaseURL == "" {
		return
	}
	peerCfg := buildPeerConfig(cacheObj.Spec.BaseURL, c.peerTLSConfig)
	c.peerClients.Add(cacheObj.Name, peerCfg)
	c.startPeerInformer(ctx, cacheObj.Name)
	c.queue.Add(cacheObj.Name)
}

// handleCacheDel reacts to a deleted Cache object (peer gone): stops the peer
// informer and removes the peer from peerClients.
func (c *GVRController) handleCacheDel(obj interface{}) {
	cacheObj, ok := obj.(*corev1alpha1.Cache)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		cacheObj, ok = tombstone.Obj.(*corev1alpha1.Cache)
		if !ok {
			return
		}
	}
	c.removePeerInformer(cacheObj.Name)
	c.peerClients.Delete(cacheObj.Name)
}

// startPeerInformer creates a dynamic informer for the GVR on the given peer and
// starts it. Idempotent: if a handle already exists the call is a no-op.
func (c *GVRController) startPeerInformer(ctx context.Context, peerName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.peerInformers[peerName]; exists {
		return
	}
	peerCfg, ok := c.peerClients.Get(peerName)
	if !ok {
		return
	}
	peerDynamic, err := kcpdynamic.NewForConfig(peerCfg)
	if err != nil {
		klog.FromContext(ctx).Error(err, "failed to create peer dynamic client", "peer", peerName)
		return
	}
	clusterInformer := kcpdynamicinformer.NewFilteredDynamicInformer(
		peerDynamic,
		c.gvr,
		resyncPeriodGVR,
		cache.Indexers{
			kcpcache.ClusterIndexName:             kcpcache.ClusterIndexFunc,
			kcpcache.ClusterAndNamespaceIndexName: kcpcache.ClusterAndNamespaceIndexFunc,
		},
		nil,
	)
	peerInformer := clusterInformer.Informer()
	// Enqueue this peer on drift so we reconcile it.
	name := peerName
	if _, err := peerInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(_ interface{}) { c.queue.Add(name) },
		UpdateFunc: func(_, _ interface{}) { c.queue.Add(name) },
		DeleteFunc: func(_ interface{}) { c.queue.Add(name) },
	}); err != nil {
		klog.FromContext(ctx).Error(err, "failed to add peer informer event handler", "peer", peerName)
		return
	}
	peerCtx, cancel := context.WithCancel(ctx)
	handle := &peerInformerHandle{
		informer:      peerInformer,
		dynamicClient: peerDynamic,
		cancel:        cancel,
	}
	c.peerInformers[peerName] = handle
	go peerInformer.Run(peerCtx.Done())
}

// removePeerInformer stops the peer informer for the given peer name and removes
// it from the peerInformers map.
func (c *GVRController) removePeerInformer(peerName string) {
	c.mu.Lock()
	handle, ok := c.peerInformers[peerName]
	delete(c.peerInformers, peerName)
	c.mu.Unlock()
	if ok {
		handle.cancel()
	}
}

// enqueueAllPeers enqueues every known peer for reconcile. Called when a source
// object changes so all peers are brought back into sync.
func (c *GVRController) enqueueAllPeers() {
	c.mu.RLock()
	peers := make([]string, 0, len(c.peerInformers))
	for name := range c.peerInformers {
		peers = append(peers, name)
	}
	c.mu.RUnlock()
	for _, name := range peers {
		c.queue.Add(name)
	}
}

func (c *GVRController) runWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *GVRController) processNextWorkItem(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	if err := c.reconcile(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("GVRController %v: reconcile peer %q failed: %w", c.gvr, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

// reconcile performs a full set-diff for this GVR between the source cache-server
// and the given peer. key is the peer name.
func (c *GVRController) reconcile(ctx context.Context, peerName string) error {
	logger := klog.FromContext(ctx)

	// Fail fast if the peer has been removed.
	if _, ok := c.peerClients.Get(peerName); !ok {
		return nil
	}

	// Get the peer informer handle and check it has synced.
	c.mu.RLock()
	handle, ok := c.peerInformers[peerName]
	c.mu.RUnlock()
	if !ok {
		return fmt.Errorf("peer informer for %q not found; will retry", peerName)
	}
	if !handle.informer.HasSynced() {
		return fmt.Errorf("peer informer for %q not yet synced; will retry", peerName)
	}

	// Build the set of authoritative shard names: shards annotated kcp.io/cache=ownName.
	authShards, err := c.authoritativeShardNames()
	if err != nil {
		return fmt.Errorf("listing shards: %w", err)
	}
	if len(authShards) == 0 {
		logger.V(4).Info("no authoritative shards; nothing to replicate")
		return nil
	}

	// Collect source objects belonging to authoritative shards.
	sourceMap := make(map[string]*unstructured.Unstructured)
	for _, raw := range c.sourceInformer.GetIndexer().List() {
		u, ok := raw.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		if !authShards[u.GetAnnotations()[clientshard.AnnotationKey]] {
			continue
		}
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(u)
		if err != nil {
			utilruntime.HandleError(err)
			continue
		}
		sourceMap[key] = u
	}

	// Collect peer objects belonging to authoritative shards.
	peerMap := make(map[string]*unstructured.Unstructured)
	for _, raw := range handle.informer.GetIndexer().List() {
		u, ok := raw.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		if !authShards[u.GetAnnotations()[clientshard.AnnotationKey]] {
			continue
		}
		key, err := kcpcache.MetaClusterNamespaceKeyFunc(u)
		if err != nil {
			utilruntime.HandleError(err)
			continue
		}
		peerMap[key] = u
	}

	// CREATE: objects in source but not on peer.
	for key, src := range sourceMap {
		if _, exists := peerMap[key]; exists {
			continue
		}
		logger.V(2).Info("creating object on peer", "peer", peerName, "key", key)
		if err := c.createOnPeer(ctx, handle, src); err != nil {
			return fmt.Errorf("create %q on peer %q: %w", key, peerName, err)
		}
	}

	// UPDATE: objects in both but content differs.
	for key, src := range sourceMap {
		peer, exists := peerMap[key]
		if !exists {
			continue
		}
		if !objectNeedsUpdate(src, peer) {
			continue
		}
		logger.V(2).Info("updating object on peer", "peer", peerName, "key", key)
		if err := c.updateOnPeer(ctx, handle, src, peer); err != nil {
			return fmt.Errorf("update %q on peer %q: %w", key, peerName, err)
		}
	}

	// DELETE: objects on peer (from authoritative shards) that are gone from source.
	for key, peer := range peerMap {
		if _, exists := sourceMap[key]; exists {
			continue
		}
		logger.V(2).Info("deleting object from peer", "peer", peerName, "key", key)
		if err := c.deleteFromPeer(ctx, handle, peer); err != nil {
			return fmt.Errorf("delete %q from peer %q: %w", key, peerName, err)
		}
	}

	return nil
}

// authoritativeShardNames returns the set of shard names that are authoritative
// for this cache-server (annotated with kcp.io/cache == ownName).
func (c *GVRController) authoritativeShardNames() (map[string]bool, error) {
	shards, err := c.shardLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	result := make(map[string]bool, len(shards))
	for _, shard := range shards {
		if shard.GetAnnotations()["kcp.io/cache"] == c.ownName {
			result[shard.GetName()] = true
		}
	}
	return result, nil
}

// objectNeedsUpdate returns true when the source and peer objects differ in any
// field other than the server-assigned ones (resourceVersion, uid, managedFields,
// creationTimestamp).
func objectNeedsUpdate(source, peer *unstructured.Unstructured) bool {
	src := source.DeepCopy()
	dst := peer.DeepCopy()
	src.SetResourceVersion("")
	dst.SetResourceVersion("")
	src.SetUID("")
	dst.SetUID("")
	src.SetManagedFields(nil)
	dst.SetManagedFields(nil)
	src.SetCreationTimestamp(metav1.Time{})
	dst.SetCreationTimestamp(metav1.Time{})
	return !reflect.DeepEqual(src.Object, dst.Object)
}

// createOnPeer creates the object on the peer, routing to the correct shard via
// context. Already-exists errors are silently ignored.
func (c *GVRController) createOnPeer(ctx context.Context, handle *peerInformerHandle, src *unstructured.Unstructured) error {
	shardName := src.GetAnnotations()[clientshard.AnnotationKey]
	cluster := logicalcluster.From(src)
	toCreate := src.DeepCopy()
	toCreate.SetResourceVersion("")
	toCreate.SetUID("")

	writeCtx := cacheclient.WithShardInContext(ctx, clientshard.New(shardName))
	_, err := handle.dynamicClient.Cluster(cluster.Path()).Resource(c.gvr).Namespace(src.GetNamespace()).Create(writeCtx, toCreate, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// updateOnPeer updates the peer's copy of the object to match the source.
// The peer's resourceVersion is preserved for optimistic concurrency.
func (c *GVRController) updateOnPeer(ctx context.Context, handle *peerInformerHandle, src, peer *unstructured.Unstructured) error {
	shardName := src.GetAnnotations()[clientshard.AnnotationKey]
	cluster := logicalcluster.From(src)
	toUpdate := src.DeepCopy()
	toUpdate.SetResourceVersion(peer.GetResourceVersion())
	toUpdate.SetUID("")

	writeCtx := cacheclient.WithShardInContext(ctx, clientshard.New(shardName))
	_, err := handle.dynamicClient.Cluster(cluster.Path()).Resource(c.gvr).Namespace(src.GetNamespace()).Update(writeCtx, toUpdate, metav1.UpdateOptions{})
	return err
}

// deleteFromPeer deletes the object from the peer. Not-found errors are silently
// ignored since the goal state is already achieved.
func (c *GVRController) deleteFromPeer(ctx context.Context, handle *peerInformerHandle, peer *unstructured.Unstructured) error {
	shardName := peer.GetAnnotations()[clientshard.AnnotationKey]
	cluster := logicalcluster.From(peer)

	writeCtx := cacheclient.WithShardInContext(ctx, clientshard.New(shardName))
	err := handle.dynamicClient.Cluster(cluster.Path()).Resource(c.gvr).Namespace(peer.GetNamespace()).Delete(writeCtx, peer.GetName(), metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
