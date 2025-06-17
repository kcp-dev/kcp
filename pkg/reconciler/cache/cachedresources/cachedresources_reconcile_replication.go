/*
Copyright 2025 The KCP Authors.

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

package cachedresources

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/informer"
	replicationcontroller "github.com/kcp-dev/kcp/pkg/reconciler/cache/cachedresources/replication"
	"github.com/kcp-dev/kcp/pkg/reconciler/dynamicrestmapper"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

// replication starts the replication machinery for a published resource.
// Or deletes the replication controller if the published resource is being deleted.
type replication struct {
	shardName                      string
	dynamicCacheClient             kcpdynamic.ClusterInterface
	kcpCacheClient                 kcpclientset.ClusterInterface
	dynRESTMapper                  *dynamicrestmapper.DynamicRESTMapper
	cacheKcpInformers              kcpinformers.SharedInformerFactory
	discoveringDynamicKcpInformers *informer.DiscoveringDynamicSharedInformerFactory
	callback                       func(obj interface{})
	controllerRegistry             *controllerRegistry
}

func (r *replication) reconcile(ctx context.Context, cachedResource *cachev1alpha1.CachedResource) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)
	logger.Info("reconciling published resource", "CachedResource", cachedResource.Name)

	gvr := schema.GroupVersionResource{
		Group:    cachedResource.Spec.Group,
		Version:  cachedResource.Spec.Version,
		Resource: cachedResource.Spec.Resource,
	}
	cluster := logicalcluster.From(cachedResource)

	var resourceLabelSelector labels.Selector
	if cachedResource.Spec.LabelSelector != nil {
		resourceLabelSelector = labels.SelectorFromSet(cachedResource.Spec.LabelSelector.MatchLabels)
	}

	clusterName := logicalcluster.From(cachedResource)
	controllerName := fmt.Sprintf("%s.%s.%s.%s.%s", clusterName, gvr.Version, gvr.Resource, gvr.Group, cachedResource.Name)
	// TODO: Add locking here when multiple workers are supported.
	controller := r.controllerRegistry.get(controllerName)
	// We setup controller even if we are deleting. This is to ensure that we can purge the cache.
	// If for some reason was dead, we will recreate it.
	if controller == nil {
		// Global informer is based on the CachedResource type and we construct index based on the schema labels.
		controllerCtx, cancel := context.WithCancel(ctx)

		global := r.cacheKcpInformers.Cache().V1alpha1().CachedObjects()

		// Local informer is based on the specific types we want to replicate.
		// TODO: use ClusterWithContext
		local, err := r.discoveringDynamicKcpInformers.Cluster(cluster).ForResource(gvr)
		if err != nil {
			logger.Error(err, "Failed to get local informer for resource", "resource", gvr)
			cancel()
			return reconcileStatusStopAndRequeue, err
		}
		replicatedKind, err := r.dynRESTMapper.ForCluster(clusterName).KindFor(gvr)
		if err != nil {
			logger.Error(err, "Failed to get Kind for resource", "resource", gvr)
			cancel()
			return reconcileStatusStopAndRequeue, err
		}
		replicated := &replicationcontroller.ReplicatedGVR{
			Kind:   replicatedKind.Kind,
			Local:  local.Informer(),
			Global: global.Informer(),
		}
		replicationcontroller.InstallIndexers(replicated)
		callback := func() {
			r.callback(cachedResource)
		}

		c, err := replicationcontroller.NewController(
			r.shardName,
			r.dynamicCacheClient,
			r.kcpCacheClient,
			gvr,
			replicated,
			callback,
			resourceLabelSelector,
		)
		if err != nil {
			cancel()
			return reconcileStatusContinue, err
		}

		go replicated.Local.Run(ctx.Done())
		go replicated.Global.Run(ctx.Done())

		if !cache.WaitForCacheSync(ctx.Done(), replicated.Local.HasSynced, replicated.Global.HasSynced) {
			cancel()
			return reconcileStatusContinue, fmt.Errorf("failed to wait for informers to sync")
		}

		go func() {
			c.Start(controllerCtx, 1)
		}()

		r.controllerRegistry.register(controllerName, c, cancel)
		if cachedResource.Status.Phase != cachev1alpha1.CachedResourcePhaseDeleting {
			conditions.MarkTrue(cachedResource, cachev1alpha1.ReplicationStarted)
			cachedResource.Status.Phase = cachev1alpha1.CachedResourcePhaseReady
		}
		return reconcileStatusStopAndRequeue, nil // Once controller is started, we requeue to check if we need to delete it.
	}
	controller.SetLabelSelector(resourceLabelSelector)

	// Check if we need to wait for cleaning. This can be few cases:
	// 1. We are in deleting phase, but nothing to delete - we are good.
	// 2. We are in deleting phase, and there is something to delete - we need to wait.
	danglingResources := cachedResource.Status.ResourceCounts != nil && cachedResource.Status.ResourceCounts.Cache > 0

	switch {
	case cachedResource.Status.Phase == cachev1alpha1.CachedResourcePhaseDeleting && danglingResources:
		controller.SetDeleted(ctx)
		return reconcileStatusStopAndRequeue, nil
	case cachedResource.Status.Phase == cachev1alpha1.CachedResourcePhaseDeleting && !danglingResources:
		r.controllerRegistry.unregister(controllerName) // unregister will cancel the context. and things will
		cachedResource.Status.Phase = cachev1alpha1.CachedResourcePhaseDeleted
		return reconcileStatusStopAndRequeue, nil
	default:
		return reconcileStatusContinue, nil
	}
}
