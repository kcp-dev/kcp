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

// deletionItem is an item in the deletion queue, representing an object
// that should be deleted, either because it was explicitly deleted or
// because it is owned by an object that is being deleted with an
// appropriate deletion propagation.
type deletionItem struct {
	reference ObjectReference
	// deletionPropagation is the deletion propagation policy to use.
	// This may be set based on the owning objects deletion propagation.
	// If empty the deletion propagation will be determined when processing
	// the item based on the object's finalizers.
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

	requeue, enqueueDeletionItems, err := gc.processDeletionQueueItem(ctx, di)
	if err != nil {
		gc.deletionQueue.AddRateLimited(di)
		return true
	}
	for _, deletionItem := range enqueueDeletionItems {
		gc.deletionQueue.Add(deletionItem)
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
		gc.deletionQueue.AddRateLimited(di)
		return true
	}

	gc.deletionQueue.Forget(di)
	return true
}

// processDeletionQueueItem processes a single item from the deletion queue.
//
// It does not interact with the deletion queue itself. Only the calling
// function interacts with the queue based on the return values of this
// function.
// This is to keep the flow of the queue processing the items and the
// deletion logic simple and separate.
func (gc *GarbageCollector) processDeletionQueueItem(ctx context.Context, di *deletionItem) (bool, []*deletionItem, error) {
	gvr, err := gc.GVR(di.reference)
	if err != nil {
		return false, nil, fmt.Errorf("error getting GVR for object %v: %w", di.reference, err)
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
			if err := gc.orphanOwnedRefs(ctx, di.reference.UID, owned); err != nil {
				return false, nil, err
			}
			return true, nil, nil
		case metav1.DeletePropagationForeground:
			// Owned resources should be foreground deleted. Enqueue them for
			// deletion and requeue the original object. This will requeue this
			// object until owned objects are gone.
			deletionItems := make([]*deletionItem, 0, len(owned))
			for i, ownedRef := range owned {
				deletionItems[i] = &deletionItem{
					reference: ownedRef,
					// Propagate the foreground deletion down the chain.
					deletionPropagation: metav1.DeletePropagationForeground,
				}
			}
			// Requeue the original object to check later if owned objects are gone.
			return true, deletionItems, nil
		case metav1.DeletePropagationBackground:
			// Owned resources should be background deleted. Enqueue
			// them for deletion and requeue the original object.
			deletionItems := make([]*deletionItem, 0, len(owned))
			for i, ownedRef := range owned {
				deletionItems[i] = &deletionItem{
					reference: ownedRef,
					// Do not set an explicit deletion propagation. This
					// will be discovered per object when it is processed.
					// deletionPropagation: ,
				}
			}
			return true, deletionItems, nil
		default:
			return false, nil, fmt.Errorf("unknown deletion propagation policy %q for object %v with owned objects", di.deletionPropagation, di.reference)
		}
	}

	latest, err := client.Get(ctx, di.reference.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Object is already gone, report success.
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}

	// No owned objects. Proceed to delete the object.
	// TODO(ntnn): scrub other gc finalizers
	if hasFinalizer(latest, metav1.FinalizerOrphanDependents) {
		patch, err := patchRemoveFinalizer(latest, metav1.FinalizerOrphanDependents)
		if err != nil {
			return false, nil, err
		}

		if _, err := client.Patch(ctx, di.reference.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return false, nil, err
		}
	}

	preconditions := metav1.Preconditions{UID: &di.reference.UID}

	if err := client.Delete(ctx, di.reference.Name, metav1.DeleteOptions{
		Preconditions: &preconditions,
	}); err != nil && !apierrors.IsNotFound(err) {
		return false, nil, err
	}

	// Deletion successful.
	return false, nil, nil
}

// orphanOwnedRefs removes the owner reference from all owned objects in the API server.
func (gc *GarbageCollector) orphanOwnedRefs(ctx context.Context, ownerUID types.UID, ownedRefs []ObjectReference) error {
	// Remove owner reference from all owned objects.
	var errs error
	for _, ownedRef := range ownedRefs {
		if err := gc.orphanOwnedRef(ctx, ownerUID, ownedRef); err != nil {
			errs = errors.Join(errs, fmt.Errorf("error orphaning owned object %v: %w", ownedRef, err))
			continue
		}
	}
	return errs
}

// orphanOwnedRef removes the owner reference from an owned object in the API server.
func (gc *GarbageCollector) orphanOwnedRef(ctx context.Context, ownerUID types.UID, ownedRef ObjectReference) error {
	gvr, err := gc.GVR(ownedRef)
	if err != nil {
		return err
	}

	client := gc.options.MetadataClusterClient.
		Cluster(ownedRef.ClusterName.Path()).
		Resource(gvr).
		Namespace(ownedRef.Namespace)

	latest, err := client.Get(ctx, ownedRef.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	patch, err := patchRemoveOwnerReference(latest, ownerUID)
	if err != nil {
		return err
	}

	if _, err := client.Patch(ctx, ownedRef.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return err
	}

	return nil
}
