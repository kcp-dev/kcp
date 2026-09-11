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
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpapiextensionsinformers "github.com/kcp-dev/client-go/apiextensions/informers"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"

	configshard "github.com/kcp-dev/kcp/config/shard"
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
	kcpFactory kcpinformers.SharedInformerFactory,
	apiExtFactory kcpapiextensionsinformers.SharedInformerFactory,
) (*RootController, error) {
	return &RootController{
		ownName:         ownName,
		peerTLSConfig:   peerTLSConfig,
		initialPeerURLs: initialPeerURLs,
		sourceConfig:    sourceConfig,
		peerClients:     newPeerClientMap(),
		cacheInformer:   kcpFactory.Core().V1alpha1().Caches().Informer(),
		crdInformer:     apiExtFactory.Apiextensions().V1().CustomResourceDefinitions().Informer(),
		shardLister:     kcpFactory.Core().V1alpha1().Shards().Lister(),
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

// seedInitialPeers contacts each URL in initialPeerURLs and adds discovered peers
// to PeerClientMap. Errors for individual URLs are logged and skipped.
func (c *RootController) seedInitialPeers(ctx context.Context) {
	logger := klog.FromContext(ctx)
	for _, url := range c.initialPeerURLs {
		peerCtx, cancel := context.WithTimeout(ctx, initialPeerTimeout)
		if err := c.seedPeersFromURL(peerCtx, url); err != nil {
			logger.Error(err, "failed to seed peers from initial URL", "url", url)
		}
		cancel()
	}
}

// seedPeersFromURL contacts one peer URL, lists all Cache objects visible on that
// peer's system:shard cluster, and registers each discovered peer in PeerClientMap.
func (c *RootController) seedPeersFromURL(ctx context.Context, url string) error {
	peerCfg := c.buildPeerConfigForSelf(url)
	peerClient, err := kcpclientset.NewForConfig(peerCfg)
	if err != nil {
		return fmt.Errorf("build client for %s: %w", url, err)
	}

	// With WithDefaultShardRoundTripper(Wildcard) applied, requests go to all shards.
	// Scoping the cluster to system:shard limits results to Cache objects that shards
	// have pulled into their local copies.
	cacheList, err := peerClient.Cluster(configshard.SystemShardCluster.Path()).CoreV1alpha1().Caches().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list Cache objects from %s: %w", url, err)
	}

	for i := range cacheList.Items {
		c.registerPeer(ctx, &cacheList.Items[i])
	}
	return nil
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
	c.peerClients.Add(obj.Name, c.buildPeerConfigForSelf(obj.Spec.BaseURL))
	logger.V(4).Info("registered peer", "peer", obj.Name, "url", obj.Spec.BaseURL)
}

// buildPeerConfigForSelf is a convenience wrapper around the package-level buildPeerConfig
// that uses this controller's TLS config.
func (c *RootController) buildPeerConfigForSelf(host string) *rest.Config {
	return buildPeerConfig(host, c.peerTLSConfig)
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
