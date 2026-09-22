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
	"sync"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"

	kcpcache "github.com/kcp-dev/kcp/pkg/cache"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	cachebootstrap "github.com/kcp-dev/kcp/pkg/cache/server/bootstrap"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerName     = "kcp-cache-syncer-root"
	initialPeerTimeout = 30 * time.Second
)

// RootController watches CRDs on the source cache-server. For each CRD it
// creates a GVRController; on CRD deletion it stops the corresponding controller.
// It has no reconcile loop of its own — its only job is lifecycle management.
type RootController struct {
	ownName         string
	peerTLSConfig   rest.TLSClientConfig
	initialPeerURLs []string

	// sourceConfig is pre-wrapped with the three cache round-trippers by the
	// caller. GVRControllers use it to create their dynamic source informers.
	sourceConfig *rest.Config

	peerClients *PeerClientMap

	// Shared informers from the caller's informer factories.
	// The root controller registers handlers; it does NOT start them.
	cacheInformer cache.SharedIndexInformer
	crdInformer   cache.SharedIndexInformer

	// shardLister is passed to each GVRController for authoritative-shard filtering.
	shardLister corev1alpha1listers.ShardClusterLister

	mu             sync.RWMutex
	gvrControllers map[schema.GroupVersionResource]*GVRController
}

// NewRootController constructs a RootController.
//
// Preconditions:
//   - sourceConfig must already be wrapped with WithCacheServiceRoundTripper,
//     WithShardNameFromContextRoundTripper, and WithDefaultShardRoundTripper(Wildcard).
//   - The Shard informer from kcpFactory must have the authoritativeshards indexer
//     registered before NewRootController is called.
//   - ownName is resolved by the caller (e.g. from Options.Extra.CacheName).
func NewRootController(
	ownName string,
	sourceConfig *rest.Config,
	peerTLSConfig rest.TLSClientConfig,
	initialPeerURLs []string,
	cacheServerInformer corev1alpha1informers.CacheClusterInformer,
	shardLister corev1alpha1listers.ShardClusterLister,
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
) (*RootController, error) {
	return &RootController{
		ownName:         ownName,
		peerTLSConfig:   peerTLSConfig,
		initialPeerURLs: initialPeerURLs,
		sourceConfig:    sourceConfig,
		peerClients:     newPeerClientMap(),
		cacheInformer:   cacheServerInformer.Informer(),
		crdInformer:     crdInformer.Informer(),
		shardLister:     shardLister,
		gvrControllers:  make(map[schema.GroupVersionResource]*GVRController),
	}, nil
}

// Start seeds the initial peer set, registers CRD event handlers, and blocks
// until ctx is cancelled. GVRControllers are created and destroyed in response
// to CRD ADD/DEL events from the shared CRD informer.
//
// Start does NOT wait for the shared informers to sync — the caller is responsible
// for ensuring the informers are running and synced before calling Start (the cache
// server's "cache-server-start-informers" post-start hook handles this).
func (c *RootController) Start(ctx context.Context) {
	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)

	fmt.Printf("### root_controller.go Start: ownName=%q initialPeerURLs=%v\n", c.ownName, c.initialPeerURLs)
	logger.Info("seeding initial peers")
	c.seedInitialPeers(ctx)

	logger.Info("registering CRD event handler")
	if _, err := c.crdInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.handleCRDAdd(ctx),
		UpdateFunc: c.handleCRDUpdate(ctx),
		DeleteFunc: c.handleCRDDel,
	}); err != nil {
		logger.Error(err, "failed to register CRD event handler; controller will not start")
		return
	}

	logger.Info("started")
	<-ctx.Done()
	logger.Info("stopped")
}

// handleCRDAdd returns a handler that starts a GVRController for the added CRD.
func (c *RootController) handleCRDAdd(ctx context.Context) func(obj interface{}) {
	return func(obj interface{}) {
		crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
		if !ok {
			return
		}
		gvr, ok := gvrFromCRD(crd)
		if !ok {
			return
		}
		go c.startGVRControllerGoroutine(ctx, gvr)
	}
}

// handleCRDUpdate returns a handler that restarts a GVRController when the GVR
// identity (group/version/resource) changes. Schema-only changes are a no-op.
func (c *RootController) handleCRDUpdate(ctx context.Context) func(oldObj, newObj interface{}) {
	return func(oldObj, newObj interface{}) {
		oldCRD, ok := oldObj.(*apiextensionsv1.CustomResourceDefinition)
		if !ok {
			return
		}
		newCRD, ok := newObj.(*apiextensionsv1.CustomResourceDefinition)
		if !ok {
			return
		}
		oldGVR, oldOK := gvrFromCRD(oldCRD)
		newGVR, newOK := gvrFromCRD(newCRD)

		if oldOK && newOK && oldGVR == newGVR {
			return // identity unchanged
		}
		if oldOK {
			c.stopGVRController(oldGVR)
		}
		if newOK {
			go c.startGVRControllerGoroutine(ctx, newGVR)
		}
	}
}

// handleCRDDel stops the GVRController for the deleted CRD.
func (c *RootController) handleCRDDel(obj interface{}) {
	crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		crd, ok = tombstone.Obj.(*apiextensionsv1.CustomResourceDefinition)
		if !ok {
			return
		}
	}
	gvr, ok := gvrFromCRD(crd)
	if !ok {
		return
	}
	c.stopGVRController(gvr)
}

// startGVRControllerGoroutine creates and starts a GVRController for the given GVR.
// It is idempotent: if a controller for this GVR already exists, it returns immediately.
// It blocks until the GVRController stops, then cleans up the map entry.
func (c *RootController) startGVRControllerGoroutine(ctx context.Context, gvr schema.GroupVersionResource) {
	c.mu.Lock()
	if _, exists := c.gvrControllers[gvr]; exists {
		c.mu.Unlock()
		return
	}
	subCtx, cancel := context.WithCancel(ctx)
	ctrl := newGVRController(gvr, c.sourceConfig, c.peerTLSConfig, c.cacheInformer, c.shardLister, c.peerClients, c.ownName, cancel)
	c.gvrControllers[gvr] = ctrl
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		// Guard against a concurrent CRD UPDATE having replaced the entry.
		if c.gvrControllers[gvr] == ctrl {
			delete(c.gvrControllers, gvr)
		}
		c.mu.Unlock()
	}()

	ctrl.Start(subCtx)
}

// stopGVRController signals the GVRController for the given GVR to stop.
// Map cleanup is handled by the goroutine running startGVRControllerGoroutine.
func (c *RootController) stopGVRController(gvr schema.GroupVersionResource) {
	c.mu.RLock()
	ctrl, ok := c.gvrControllers[gvr]
	c.mu.RUnlock()
	if ok {
		ctrl.Stop()
	}
}

// seedInitialPeers launches one background goroutine per initial peer URL.
// Each goroutine polls the peer (5 s interval) until at least one Cache object is
// returned, registers all found peers, then exits. The goroutines are bounded by ctx.
func (c *RootController) seedInitialPeers(ctx context.Context) {
	for _, url := range c.initialPeerURLs {
		url := url
		go func() {
			logger := klog.FromContext(ctx).WithValues("url", url)
			_ = wait.PollUntilContextCancel(ctx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
				reqCtx, cancel := context.WithTimeout(ctx, initialPeerTimeout)
				defer cancel()
				found, err := c.seedPeersFromURL(reqCtx, url)
				if err != nil {
					logger.V(4).Info("initial peer not yet reachable, retrying", "err", err)
					return false, nil
				}
				if !found {
					logger.V(4).Info("initial peer reachable but no Cache objects yet, retrying")
					return false, nil
				}
				logger.Info("initial peer seeded successfully")
				return true, nil
			})
		}()
	}
}

// seedPeersFromURL contacts one peer URL, lists all Cache objects visible on that
// peer's system:shard cluster, and registers each discovered peer in PeerClientMap.
// Returns true if at least one Cache object was present in the response.
func (c *RootController) seedPeersFromURL(ctx context.Context, url string) (bool, error) {
	peerCfg := buildPeerConfig(url, c.peerTLSConfig)
	peerClient, err := kcpclientset.NewForConfig(peerCfg)
	if err != nil {
		return false, fmt.Errorf("build client for %s: %w", url, err)
	}

	// Enqueue only the peer itself. Its own discovered peers will trickle down during steady-state reconciles.
	ctx = cacheclient.WithShardInContext(ctx, cachebootstrap.SystemCacheServerShard)
	cacheList, err := peerClient.Cluster(kcpcache.SystemCacheCluster.Path()).CoreV1alpha1().Caches().List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("list Cache objects from %s: %w", url, err)
	}
	names := make([]string, len(cacheList.Items))
	for i := range cacheList.Items {
		names[i] = cacheList.Items[i].Name
	}
	fmt.Printf("### seedPeersFromURL: got %v from %q\n", names, url)
	if len(cacheList.Items) != 1 {
		// TODO: before signaling ready, cache-server should ensure the CacheServer obj in
		// system:cache:server/system:shard is the one identifying that cache, otherwise
		// we could register something that doesn't exist and never reconcile.
		return false, nil
	}

	c.registerPeer(ctx, &cacheList.Items[0])

	return true, nil
}

// registerPeer validates a Cache object and, if it represents a valid remote peer,
// adds it to PeerClientMap. Self-references and objects with empty BaseURL are skipped.
func (c *RootController) registerPeer(ctx context.Context, obj *corev1alpha1.Cache) {
	logger := klog.FromContext(ctx)
	if obj.Name == c.ownName {
		return
	}
	if obj.Spec.BaseURL == "" {
		logger.V(4).Info("skipping Cache object with empty BaseURL", "name", obj.Name)
		return
	}
	c.peerClients.Add(obj.Name, buildPeerConfig(obj.Spec.BaseURL, c.peerTLSConfig))
	logger. /*V(4).*/ Info("registered peer", "peer", obj.Name, "url", obj.Spec.BaseURL)
}

// gvrFromCRD extracts the GroupVersionResource from a CRD, using the storage version.
func gvrFromCRD(crd *apiextensionsv1.CustomResourceDefinition) (schema.GroupVersionResource, bool) {
	for _, v := range crd.Spec.Versions {
		if v.Storage {
			return schema.GroupVersionResource{
				Group:    crd.Spec.Group,
				Version:  v.Name,
				Resource: crd.Spec.Names.Plural,
			}, true
		}
	}
	return schema.GroupVersionResource{}, false
}
