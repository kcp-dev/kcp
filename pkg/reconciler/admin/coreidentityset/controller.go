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

package coreidentityset

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	adminv1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/admin/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/identity/corekey"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
	"github.com/kcp-dev/kcp/pkg/server/bootstrap"
)

const (
	ControllerName = "kcp-core-identity-set"
	workKey        = "key"

	// resyncInterval bounds how long a change on another shard stays
	// unnoticed if an event was missed.
	resyncInterval = time.Minute
)

// Config holds what the controller needs to know about its shard.
type Config struct {
	ShardName string
	// Key is the mounted core identities key, or nil.
	Key *corekey.Key
	// Identities are the shard's resolved core identities.
	Identities *bootstrap.Identities
	// Health is updated from every published CoreIdentitySet. Optional.
	Health *Health
	// Conflict reports a configuration conflict to publish, e.g. a
	// --root-identities-file entry disagreeing with an existing identity.
	// Optional.
	Conflict func() string
}

// NewController returns a controller that keeps the shard's CoreIdentitySet
// up to date: it republishes on changes of the local object, of any other
// shard's object (through the cache server) and of the root workspace's
// APIExports (through the cache server).
func NewController(
	cfg Config,
	kcpClusterClient kcpclientset.ClusterInterface,
	localSetInformer adminv1alpha1informers.CoreIdentitySetClusterInformer,
	globalSetInformer adminv1alpha1informers.CoreIdentitySetClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
) (*Controller, error) {
	c := &Controller{
		cfg: cfg,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: ControllerName},
		),
		kcpClusterClient: kcpClusterClient,
		listGlobalSets: func() ([]*adminv1alpha1.CoreIdentitySet, error) {
			return globalSetInformer.Lister().List(labels.Everything())
		},
		listRootExports: func() ([]*apisv1alpha2.APIExport, error) {
			return globalAPIExportInformer.Lister().Cluster(core.RootCluster).List(labels.Everything())
		},
	}

	enqueue := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.queue.Add(workKey) },
		UpdateFunc: func(old, new interface{}) { c.queue.Add(workKey) },
		DeleteFunc: func(obj interface{}) { c.queue.Add(workKey) },
	}
	_, _ = localSetInformer.Informer().AddEventHandler(events.WithoutSyncs(enqueue))
	_, _ = globalSetInformer.Informer().AddEventHandler(events.WithoutSyncs(enqueue))
	_, _ = globalAPIExportInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			export, ok := obj.(*apisv1alpha2.APIExport)
			if !ok {
				if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
					export, ok = tombstone.Obj.(*apisv1alpha2.APIExport)
					if !ok {
						return false
					}
				} else {
					return false
				}
			}
			return logicalcluster.From(export) == core.RootCluster
		},
		Handler: enqueue,
	}))

	return c, nil
}

// Controller maintains the shard's CoreIdentitySet.
type Controller struct {
	cfg   Config
	queue workqueue.TypedRateLimitingInterface[string]

	kcpClusterClient kcpclientset.ClusterInterface
	listGlobalSets   func() ([]*adminv1alpha1.CoreIdentitySet, error)
	listRootExports  func() ([]*apisv1alpha2.APIExport, error)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *Controller) Start(ctx context.Context, _ int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	c.queue.Add(workKey)
	go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	go wait.UntilWithContext(ctx, func(context.Context) { c.queue.Add(workKey) }, resyncInterval)

	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)

	if err := c.reconcile(ctx); err != nil {
		utilruntime.HandleError(fmt.Errorf("%s failed with: %w", ControllerName, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) reconcile(ctx context.Context) error {
	in, err := c.input()
	if err != nil {
		return err
	}
	set, err := Publish(ctx, c.kcpClusterClient, in)
	if err != nil {
		return err
	}
	if c.cfg.Health != nil {
		c.cfg.Health.Update(set)
	}
	return nil
}

// input gathers everything Apply needs from the shard and the cache server.
func (c *Controller) input() (Input, error) {
	in := Input{
		ShardName:   c.cfg.ShardName,
		ExportNames: c.cfg.Identities.ExportNames(),
		Identities:  c.cfg.Identities.Snapshot(),
	}
	if c.cfg.Key != nil {
		in.ObservedKeyID = c.cfg.Key.ID()
	}
	if c.cfg.Conflict != nil {
		in.Conflict = c.cfg.Conflict()
	}

	sets, err := c.listGlobalSets()
	if err != nil {
		return Input{}, err
	}
	for _, set := range sets {
		if set.Status.Shard == c.cfg.ShardName || set.Name == c.cfg.ShardName {
			continue
		}
		in.Peers = append(in.Peers, *set)
	}

	in.RootView = RootView(sets, func() ([]*apisv1alpha2.APIExport, error) { return c.listRootExports() })
	return in, nil
}

// RootView builds the root shard's view of the core identities from the
// root shard's CoreIdentitySet, if present, falling back to the root
// workspace's APIExports (both as replicated to the cache server). It returns
// nil when neither is visible.
func RootView(sets []*adminv1alpha1.CoreIdentitySet, listRootExports func() ([]*apisv1alpha2.APIExport, error)) map[string]string {
	for _, set := range sets {
		if set.Status.Shard != corev1alpha1.RootShard {
			continue
		}
		view := map[string]string{}
		for _, export := range set.Status.Exports {
			if export.IdentityHash != "" {
				view[export.Name] = export.IdentityHash
			}
		}
		if len(view) > 0 {
			return view
		}
	}

	exports, err := listRootExports()
	if err != nil {
		return nil
	}
	view := map[string]string{}
	for _, export := range exports {
		if export.Status.IdentityHash != "" {
			view[export.Name] = export.Status.IdentityHash
		}
	}
	if len(view) == 0 {
		return nil
	}
	return view
}
