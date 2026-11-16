/*
Copyright 2025 The kcp Authors.

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

package clustercachedresources

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	"github.com/kcp-dev/kcp/pkg/informer"
	replicationcontroller "github.com/kcp-dev/kcp/pkg/reconciler/cache/clustercachedresources/replication"
)

// replication starts the replication machinery for a published resource.
// Or deletes the replication controller if the published resource is being deleted.
type replication struct {
	shardName                            string
	localDynamicClusterClient            kcpdynamic.ClusterInterface
	globalDynamicClusterClient           kcpdynamic.ClusterInterface
	kcpCacheClient                       kcpclientset.ClusterInterface
	getKind                              func(cluster logicalcluster.Name, gvr schema.GroupVersionResource) (schema.GroupVersionKind, error)
	localDiscoveringDynamicKcpInformers  *informer.DiscoveringDynamicSharedInformerFactory
	globalDiscoveringDynamicKcpInformers *informer.DiscoveringDynamicSharedInformerFactory
	cacheApiExtensionsClusterClient      kcpapiextensionsclientset.ClusterInterface
	requeueSelf                          func(obj interface{})
	controllerRegistry                   *controllerRegistry
}

func (r *replication) reconcile(ctx context.Context, clusterCachedResource *cachev1alpha1.ClusterCachedResource) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)
	logger.Info("reconciling cached resource", "ClusterCachedResource", clusterCachedResource.Name)

	gvr := schema.GroupVersionResource{
		Group:    clusterCachedResource.Spec.Group,
		Version:  clusterCachedResource.Spec.Version,
		Resource: clusterCachedResource.Spec.Resource,
	}
	cluster := logicalcluster.From(clusterCachedResource)

	selection := replicationcontroller.SelectionFor(clusterCachedResource)

	clusterName := logicalcluster.From(clusterCachedResource)
	controllerName := controllerNameFor(clusterCachedResource)
	// TODO: Add locking here when multiple workers are supported.
	controller := r.controllerRegistry.get(controllerName)
	// We setup controller even if we are deleting. This is to ensure that we can purge the cache.
	// If for some reason was dead, we will recreate it.
	danglingResources := clusterCachedResource.Status.ResourceCounts != nil && clusterCachedResource.Status.ResourceCounts.Cache > 0
	if controller == nil {
		if clusterCachedResource.DeletionTimestamp != nil && !danglingResources {
			clusterCachedResource.Status.Phase = cachev1alpha1.ClusterCachedResourcePhaseDeleted
			return reconcileStatusStopAndRequeue, nil
		}

		// Get kind first, so we don't create informers for resources which are not served.
		replicatedKind, err := r.getKind(clusterName, gvr)
		if err != nil {
			logger.Error(err, "Failed to get Kind for resource", "resource", gvr)
			return reconcileStatusStopAndRequeue, err
		}
		// The REST mapper can still have a CRD which the local factory already removed.
		// ForResource would then create an informer the factory never stops.
		if !r.servedLocally(gvr) {
			return reconcileStatusStopAndRequeue, fmt.Errorf("resource %s is not served by local informers", gvr)
		}

		global, err := r.globalDiscoveringDynamicKcpInformers.ForResource(gvr)
		if err != nil {
			logger.Error(err, "Failed to get global informer for resource", "resource", gvr)
			return reconcileStatusStopAndRequeue, err
		}

		// Local informer is based on the specific types we want to replicate.
		local, err := r.localDiscoveringDynamicKcpInformers.ForResource(gvr)
		if err != nil {
			logger.Error(err, "Failed to get local informer for resource", "resource", gvr)
			return reconcileStatusStopAndRequeue, err
		}
		replicated := &replicationcontroller.ReplicatedGVR{
			Identity: clusterCachedResource.Status.IdentityHash,
			Kind:     replicatedKind.Kind,
			Local:    local.Informer(),
			Global:   global.Informer(),
		}
		if replicated.Local.IsStopped() || replicated.Global.IsStopped() {
			return reconcileStatusStopAndRequeue, fmt.Errorf("informer for %s is stopped", gvr)
		}
		replicationcontroller.InstallIndexers(replicated)

		// Run informers with the factories' stop channels, not ctx. ctx is cancelled when we
		// lose the leader lease and a stopped informer can't be reused. The global factory
		// has no worker, so global informers run until the process exits.
		r.localDiscoveringDynamicKcpInformers.Start(nil)
		r.globalDiscoveringDynamicKcpInformers.Start(nil)

		requeueSelf := func() {
			r.requeueSelf(clusterCachedResource)
		}

		c := replicationcontroller.NewController(
			r.shardName,
			r.localDynamicClusterClient,
			r.globalDynamicClusterClient,
			r.kcpCacheClient,
			r.cacheApiExtensionsClusterClient,
			cluster,
			gvr,
			replicated,
			requeueSelf,
			selection,
		)

		controllerCtx, cancel := context.WithCancel(ctx)
		r.controllerRegistry.register(controllerName, c, cancel)

		go func() {
			c.Start(controllerCtx, 1)
			removed := r.controllerRegistry.unregisterIf(controllerName, c)
			cancel()
			// controller stopped by itself (e.g. CRD was deleted), recreate it
			if removed && ctx.Err() == nil {
				logger.Info("Replication controller stopped, recreating", "controller", controllerName)
				requeueSelf()
			}
		}()

		return reconcileStatusStopAndRequeue, nil // Once controller is started, we requeue to check if we need to delete it.
	}
	controller.SetSelection(selection)

	// Check if we need to wait for cleaning. This can be few cases:
	// 1. We are in deleting phase, but nothing to delete - we are good.
	// 2. We are in deleting phase, and there is something to delete - we need to wait.

	switch {
	case clusterCachedResource.Status.Phase == cachev1alpha1.ClusterCachedResourcePhaseDeleting && danglingResources:
		controller.SetDeleted(ctx)
		return reconcileStatusStopAndRequeue, nil
	case clusterCachedResource.Status.Phase == cachev1alpha1.ClusterCachedResourcePhaseDeleting && !danglingResources:
		r.controllerRegistry.unregister(controllerName) // unregister will cancel the context. and things will
		clusterCachedResource.Status.Phase = cachev1alpha1.ClusterCachedResourcePhaseDeleted
		return reconcileStatusStopAndRequeue, nil
	default:
		return reconcileStatusContinue, nil
	}
}

func (r *replication) servedLocally(gvr schema.GroupVersionResource) bool {
	resources, err := r.localDiscoveringDynamicKcpInformers.ServerResourcesForGroupVersion(gvr.GroupVersion().String())
	if err != nil {
		return false
	}
	for _, resource := range resources.APIResources {
		if resource.Name == gvr.Resource {
			return true
		}
	}
	return false
}

func controllerNameFor(ccr *cachev1alpha1.ClusterCachedResource) string {
	return fmt.Sprintf("%s.%s.%s.%s.%s", logicalcluster.From(ccr), ccr.Spec.Version, ccr.Spec.Resource, ccr.Spec.Group, ccr.Name)
}
