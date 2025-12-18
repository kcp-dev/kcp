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

package garbagecollector

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
)

type deletionItem struct {
	reference           ObjectReference
	deletionPropagation metav1.DeletionPropagation
}

func (gc *GarbageCollector) startDeletion(ctx context.Context) {
	// TODO(ntnn): Scale workers dynamically.
	//
	// One option would be scaling based off of the number of
	// workspaces, e.g. logarithmic scaling based off of the number
	// of workspaces on this shard or total number of workspaces.
	// With lower and upper bound configurable.
	//
	// Could also scale off of queue length, though that could be more
	// expensive than just having a larger number of deletion worker
	// goroutines running.
	for range gc.options.DeletionWorkers {
		go wait.UntilWithContext(ctx, gc.runDeletionQueueWorker, time.Second)
	}
}

func (gc *GarbageCollector) runDeletionQueueWorker(ctx context.Context) {
	for gc.processDeletionQueue(ctx) {
	}
}

func (gc *GarbageCollector) processDeletionQueue(ctx context.Context) bool {
	di, shutdown := gc.deletionQueue.Get()
	if shutdown {
		return false
	}
	defer gc.deletionQueue.Done(di)

	gc.log.Info("processing deletion queue item", "object", di.reference)
	requeue, err := gc.processDeletionQueueItem(ctx, di)
	if err != nil {
		gc.log.Error(err, "error processing deletion queue item", "object", di.reference)
		gc.deletionQueue.AddRateLimited(di)
		return true
	}
	if requeue {
		gc.deletionQueue.Add(di)
		return true
	}

	// This is more of a failsafe. If the processDeletionQueueItem
	// returned no error and no requeue the object should be gone from
	// the API server and removable from the graph.
	//
	// If this is not the case something went wrong, to be safe we
	// requeue the object for reprocessing.
	success, owned := gc.graph.Remove(di.reference)
	if !success || len(owned) > 0 {
		gc.log.Error(nil, "owned objects still exist, requeuing", "object", di.reference, "graphRemovalSuccess", success, "ownedCount", len(owned))
		gc.deletionQueue.AddRateLimited(di)
		return true
	}

	gc.deletionQueue.Forget(di)
	return true
}

func (gc *GarbageCollector) processDeletionQueueItem(ctx context.Context, di *deletionItem) (bool, error) {
	gvr, err := gc.GVR(di.reference)
	if err != nil {
		return false, fmt.Errorf("error getting GVR for object %v: %w", di.reference, err)
	}

	client := gc.options.MetadataClusterClient.Cluster(di.reference.ClusterName.Path()).
		Resource(gvr).
		Namespace(di.reference.Namespace)

	if string(di.deletionPropagation) == "" {
		// Deletion propagation is empty. This is likely because the
		// object was queued for deletion as an owned object without
		// foreground deletion.
		// If the object is still available in the api server we
		// determine the propagation from there, otherwise default to
		// background deletion.
		di.deletionPropagation = metav1.DeletePropagationBackground
		if latest, err := client.Get(ctx, di.reference.Name, metav1.GetOptions{}); err == nil {
			di.deletionPropagation = deletionPropagationFromFinalizers(latest)
		}
	}

	// Grab any owned objects from the graph
	owned := gc.graph.Owned(di.reference)
	if len(owned) > 0 {
		switch di.deletionPropagation {
		case metav1.DeletePropagationOrphan:
			// Orphan owned resources and requeue until the graph lists no owned objects.
			if err := gc.orphanOwned(ctx, di.reference, owned); err != nil {
				return false, err
			}
			return true, nil
		case metav1.DeletePropagationForeground:
			// Owned resources should be foreground deleted. Enqueue them for
			// deletion and requeue the original object. This will requeue this
			// object until owned objects are gone.
			for _, ownedRef := range owned {
				gc.log.Info("queuing owned object for deletion", "ownedObject", ownedRef, "ownerObject", di.reference)
				gc.deletionQueue.Add(&deletionItem{
					reference: ownedRef,
					// Propagate the foreground deletion down the chain.
					deletionPropagation: metav1.DeletePropagationForeground,
				})
			}
			// Requeue the original object to check later if owned objects are gone.
			gc.log.Info("owned objects exist, requeuing deletion", "object", di.reference, "ownedCount", len(owned))
			return true, nil
		case metav1.DeletePropagationBackground:
			// Owned resources should be background deleted. Enqueue them for
			// deletion, but do not requeue the original object as it can be
			// deleted right away.
			for _, ownedRef := range owned {
				gc.log.Info("queuing owned object for deletion", "ownedObject", ownedRef, "ownerObject", di.reference)
				gc.deletionQueue.Add(&deletionItem{
					reference: ownedRef,
					// Do not set an explicit deletion propagation. This
					// will be discovered per object when it is processed.
					// deletionPropagation: ,
				})
			}
		default:
			return false, fmt.Errorf("unknown deletion propagation policy %q for object %v with owned objects", di.deletionPropagation, di.reference)
		}
	}

	latest, err := client.Get(ctx, di.reference.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Object is already gone, report success.
		return false, nil
	}
	if err != nil {
		return false, err
	}

	// No owned objects. Proceed to delete the object.
	gc.log.Info("deleting object from API server", "object", di.reference)

	// TODO(ntnn): scrub other gc finalizers
	if hasFinalizer(latest, metav1.FinalizerOrphanDependents) {
		gc.log.Info("removing orphan finalizer from object", "object", di.reference)
		patch, err := patchRemoveFinalizer(latest, metav1.FinalizerOrphanDependents)
		if err != nil {
			return false, err
		}

		if _, err := client.Patch(ctx, di.reference.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return false, err
		}
	}

	preconditions := metav1.Preconditions{UID: &di.reference.UID}

	if err := client.Delete(ctx, di.reference.Name, metav1.DeleteOptions{
		Preconditions: &preconditions,
	}); err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}

	// Deletion successful.
	return false, nil
}

func (gc *GarbageCollector) orphanOwned(ctx context.Context, or ObjectReference, owned []ObjectReference) error {
	// Remove owner reference from all owned objects.
	var errs error
	for _, ownedRef := range owned {
		gvr, err := gc.GVR(ownedRef)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}

		client := gc.options.MetadataClusterClient.
			Cluster(ownedRef.ClusterName.Path()).
			Resource(gvr).
			Namespace(ownedRef.Namespace)

		latest, err := client.Get(ctx, ownedRef.Name, metav1.GetOptions{})
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}

		patch, err := patchRemoveOwnerReference(latest, or.UID)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}

		if _, err := client.Patch(ctx, ownedRef.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
			errs = errors.Join(errs, err)
			continue
		}
	}
	return errs
}
