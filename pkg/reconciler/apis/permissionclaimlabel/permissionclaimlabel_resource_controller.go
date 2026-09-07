/*
Copyright 2022 The kcp Authors.

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

package permissionclaimlabel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/permissionclaim"
)

const (
	ResourceControllerName = "kcp-resource-permissionclaimlabel"
)

// NewResourceController returns a new controller for labeling resources for accepted permission claims.
func NewResourceController(
	kcpClusterClient kcpclientset.ClusterInterface,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	dynamicDiscoverySharedInformerFactory *informer.DiscoveringDynamicSharedInformerFactory,
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	apiExportInformer, globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
) (*resourceController, error) {
	c := &resourceController{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ResourceControllerName,
			},
		),
		kcpClusterClient:       kcpClusterClient,
		dynamicClusterClient:   dynamicClusterClient,
		ddsif:                  dynamicDiscoverySharedInformerFactory,
		apiBindingsIndexer:     apiBindingInformer.Informer().GetIndexer(),
		permissionClaimLabeler: permissionclaim.NewLabeler(apiBindingInformer, apiExportInformer, globalAPIExportInformer),
	}

	logger := logging.WithReconciler(klog.Background(), ControllerName)
	c.ddsif.AddEventHandler(informer.GVREventHandlerFuncs{
		AddFunc:    func(gvr schema.GroupVersionResource, obj interface{}) { c.enqueueForResource(logger, gvr, obj) },
		UpdateFunc: func(gvr schema.GroupVersionResource, _, obj interface{}) { c.enqueueForResource(logger, gvr, obj) },
		DeleteFunc: nil, // Nothing to do.
	})

	// Claim labels embed the claimed identity hash, normalized to the
	// export's canonical identity. When an export's identity is rotated or
	// an alias is registered/retired, the correct labels of every object
	// claimed under that identity change without any event on the objects
	// themselves, so re-enqueue them explicitly. (Re-reconciling the claiming
	// APIBindings is not enough: they only touch objects when their set of
	// applied claims changes, which an identity change does not.)
	exportIdentityHandler := cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldExport, ok := oldObj.(*apisv1alpha2.APIExport)
			if !ok {
				return
			}
			newExport, ok := newObj.(*apisv1alpha2.APIExport)
			if !ok {
				return
			}
			if oldExport.Status.IdentityHash == newExport.Status.IdentityHash &&
				slices.Equal(oldExport.Status.IdentityAliasHashes, newExport.Status.IdentityAliasHashes) {
				return
			}
			for _, resource := range newExport.Spec.Resources {
				c.enqueueClaimedResources(logger, schema.GroupResource{Group: resource.Group, Resource: resource.Name})
			}
		},
	}
	_, _ = apiExportInformer.Informer().AddEventHandler(exportIdentityHandler)
	_, _ = globalAPIExportInformer.Informer().AddEventHandler(exportIdentityHandler)

	return c, nil
}

// resourceController reconciles resources from the ddsif, and will determine if it needs
// its permission claim labels updated.
type resourceController struct {
	queue                  workqueue.TypedRateLimitingInterface[string]
	kcpClusterClient       kcpclientset.ClusterInterface
	dynamicClusterClient   kcpdynamic.ClusterInterface
	ddsif                  *informer.DiscoveringDynamicSharedInformerFactory
	apiBindingsIndexer     cache.Indexer
	permissionClaimLabeler *permissionclaim.Labeler
}

// enqueueClaimedResources enqueues every object of the group resource in
// every logical cluster holding an APIBinding that accepts a claim on it.
func (c *resourceController) enqueueClaimedResources(logger logr.Logger, gr schema.GroupResource) {
	bindings, err := indexers.ListAPIBindingsByAcceptedClaimedGroupResource(c.apiBindingsIndexer, gr)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("failed to list APIBindings by claimed group resource %q: %w", gr, err))
		return
	}
	if len(bindings) == 0 {
		return
	}
	clusters := sets.New[logicalcluster.Name]()
	for _, binding := range bindings {
		clusters.Insert(logicalcluster.From(binding))
	}

	informers, _ := c.ddsif.Informers()
	for gvr := range informers {
		if gvr.Group != gr.Group || gvr.Resource != gr.Resource {
			continue
		}
		inf, err := c.ddsif.ForResource(gvr)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("failed to get informer for %q: %w", gvr, err))
			return
		}
		count := 0
		for _, cluster := range sets.List(clusters) {
			objs, err := inf.Lister().ByCluster(cluster).List(labels.Everything())
			if err != nil {
				utilruntime.HandleError(fmt.Errorf("failed to list %q in %q: %w", gvr, cluster, err))
				continue
			}
			for _, obj := range objs {
				c.enqueueForResource(logger, gvr, obj)
				count++
			}
		}
		logger.V(4).Info("re-enqueueing claimed resources after identity change", "groupResource", gr.String(), "count", count)
		return // one served version is enough, labels are version-agnostic
	}
}

// enqueueForResource adds the resource (gvr + obj) to the queue.
func (c *resourceController) enqueueForResource(logger logr.Logger, gvr schema.GroupVersionResource, obj interface{}) {
	queueKey := strings.Join([]string{gvr.Resource, gvr.Version, gvr.Group}, ".")

	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	queueKey += "::" + key
	logging.WithQueueKey(logger, queueKey).V(5).Info("queuing resource")
	c.queue.Add(queueKey)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *resourceController) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ResourceControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("starting controller")
	defer logger.Info("shutting down controller")

	for range numThreads {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *resourceController) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *resourceController) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(5).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ResourceControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *resourceController) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	parts := strings.SplitN(key, "::", 2)
	if len(parts) != 2 {
		logger.Error(errors.New("unexpected key format"), "skipping key")
		return nil
	}

	gvr, _ := schema.ParseResourceArg(parts[0])
	if gvr == nil {
		logger.Error(errors.New("unable to parse gvr string"), "skipping key", "gvr", parts[0])
		return nil
	}
	key = parts[1]

	logger = logger.WithValues("gvr", gvr.String(), "name", key)

	inf, err := c.ddsif.ForResource(*gvr)
	if err != nil {
		return fmt.Errorf("error getting dynamic informer for GVR %q: %w", gvr, err)
	}

	obj, exists, err := inf.Informer().GetIndexer().GetByKey(key)
	if err != nil {
		logger.Error(err, "unable to get from indexer")
		return nil // retrying won't help
	}
	if !exists {
		logger.V(4).Info("resource not found")
		return nil
	}

	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		logger.Error(nil, "got unexpected type", "type", fmt.Sprintf("%T", obj))
		return nil // retrying won't help
	}
	u = u.DeepCopy()

	logger = logging.WithObject(logger, u)
	ctx = klog.NewContext(ctx, logger)

	return c.reconcile(ctx, u, gvr)
}
