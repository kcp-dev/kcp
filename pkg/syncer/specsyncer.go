/*
Copyright 2021 The KCP Authors.

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

package syncer

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
)

func deepEqualApartFromStatus(oldObj, newObj interface{}) bool {
	oldUnstrob, isOldObjUnstructured := oldObj.(*unstructured.Unstructured)
	newUnstrob, isNewObjUnstructured := newObj.(*unstructured.Unstructured)
	if !isOldObjUnstructured || !isNewObjUnstructured {
		return false
	}

	// TODO(jmprusi): Remove this after switching to virtual workspaces.
	// remove status annotation from oldObj and newObj before comparing
	oldAnnotations := oldUnstrob.GetAnnotations()
	for k := range oldAnnotations {
		if strings.HasPrefix(k, ExperimentalStatusAnnotation) {
			delete(oldAnnotations, k)
		}
	}
	oldUnstrob.SetAnnotations(oldAnnotations)

	newAnnotations := newUnstrob.GetAnnotations()
	for k := range newAnnotations {
		if strings.HasPrefix(k, ExperimentalStatusAnnotation) {
			delete(newAnnotations, k)
		}
	}
	newUnstrob.SetAnnotations(newAnnotations)

	if !equality.Semantic.DeepEqual(oldUnstrob.GetAnnotations(), newUnstrob.GetAnnotations()) {
		return false
	}
	if !equality.Semantic.DeepEqual(oldUnstrob.GetLabels(), newUnstrob.GetLabels()) {
		return false
	}
	if !equality.Semantic.DeepEqual(oldUnstrob.GetFinalizers(), newUnstrob.GetFinalizers()) {
		return false
	}

	oldIsBeingDeleted := oldUnstrob.GetDeletionTimestamp() != nil
	newIsBeingDeleted := newUnstrob.GetDeletionTimestamp() != nil
	if oldIsBeingDeleted != newIsBeingDeleted {
		return false
	}

	oldObjKeys := sets.StringKeySet(oldUnstrob.UnstructuredContent())
	newObjKeys := sets.StringKeySet(newUnstrob.UnstructuredContent())
	for _, key := range oldObjKeys.Union(newObjKeys).UnsortedList() {
		if key == "metadata" || key == "status" {
			continue
		}
		if !equality.Semantic.DeepEqual(oldUnstrob.UnstructuredContent()[key], newUnstrob.UnstructuredContent()[key]) {
			return false
		}
	}
	return true
}

const specSyncerAgent = "kcp#spec-syncer/v0.0.0"

func NewSpecSyncer(from, to *rest.Config, gvrs []string, kcpClusterName logicalcluster.LogicalCluster, pclusterID string) (*Controller, error) {
	from = rest.CopyConfig(from)
	from.UserAgent = specSyncerAgent
	to = rest.CopyConfig(to)
	to.UserAgent = specSyncerAgent

	fromClients, err := dynamic.NewClusterForConfig(from)
	if err != nil {
		return nil, err
	}
	fromClient := fromClients.Cluster(kcpClusterName)
	toClient := dynamic.NewForConfigOrDie(to)

	// Register the default mutators
	mutatorsMap := getDefaultMutators(from)

	return New(kcpClusterName, pclusterID, fromClient, toClient, SyncDown, gvrs, pclusterID, mutatorsMap)
}

func (c *Controller) deleteFromDownstream(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	// TODO: get UID of just-deleted object and pass it as a precondition on this delete.
	// This would avoid races where an object is deleted and another object with the same name is created immediately after.

	if err := c.toClient.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{}); errors.IsNotFound(err) {
		return nil
	} else {
		return err
	}
}

const namespaceLocatorAnnotation = "kcp.dev/namespace-locator"

// TODO: This function is there as a quick and dirty implementation of namespace creation.
//       In fact We should also be getting notifications about namespaces created upstream and be creating downstream equivalents.
func (c *Controller) ensureDownstreamNamespaceExists(ctx context.Context, downstreamNamespace string, upstreamObj *unstructured.Unstructured) error {
	namespaces := c.toClient.Resource(schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "namespaces",
	})

	newNamespace := &unstructured.Unstructured{}
	newNamespace.SetAPIVersion("v1")
	newNamespace.SetKind("Namespace")
	newNamespace.SetName(downstreamNamespace)

	// TODO: if the downstream namespace loses these annotations/labels after creation,
	// we don't have anything in place currently that will put them back.
	l := NamespaceLocator{
		LogicalCluster: logicalcluster.From(upstreamObj),
		Namespace:      upstreamObj.GetNamespace(),
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	newNamespace.SetAnnotations(map[string]string{
		namespaceLocatorAnnotation: string(b),
	})

	if upstreamObj.GetLabels() != nil {
		newNamespace.SetLabels(map[string]string{
			// TODO: this should be set once at syncer startup and propagated around everywhere.
			workloadClusterLabelName(c.pcluster): "",
		})
	}

	if _, err := namespaces.Create(ctx, newNamespace, metav1.CreateOptions{}); err != nil {
		// An already exists error is ok - it means something else beat us to creating the namespace.
		if !k8serrors.IsAlreadyExists(err) {
			// Any other error is not good, though.
			// TODO bubble this up as a condition somewhere.
			klog.Errorf("Error while creating namespace %q: %v", downstreamNamespace, err)
			return err
		}
	} else {
		klog.Infof("Created downstream namespace %s for upstream namespace %s|%s", downstreamNamespace, c.upstreamClusterName, upstreamObj.GetNamespace())
	}

	return nil
}

func (c *Controller) notifySyncerOwnership(ctx context.Context, gvr schema.GroupVersionResource, upstreamObj *unstructured.Unstructured) error {
	name := upstreamObj.GetName()
	namespace := upstreamObj.GetNamespace()

	// TODO(jmprusi): We add finalizers to the resources in the namespace, not the namespace itself to avoid blocking
	//                on namespace deletion. Do we need finalizers on namespaces?
	if gvr.Resource != "namespaces" && gvr.Group != "" {
		c.setSyncerFinalizer(upstreamObj)
	}

	if _, err := c.fromClient.Resource(gvr).Namespace(namespace).Update(ctx, upstreamObj, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("Failed updating to notify syncer ownership on resource %s|%s/%s: %v", c.upstreamClusterName, namespace, name, err)
		return err
	}
	klog.Infof("Updated resource %s|%s/%s to notify syncer ownership", c.upstreamClusterName, namespace, name)
	return nil
}

func (c *Controller) getLocationFinalizer(obj *unstructured.Unstructured) (string, bool) {
	val, ok := obj.GetAnnotations()[LocationFinalizersAnnotationName(c.pcluster)]
	return val, ok
}

func (c *Controller) applyToDownstream(ctx context.Context, eventType watch.EventType, gvr schema.GroupVersionResource, downstreamNamespace string, upstreamObj *unstructured.Unstructured) error {
	if err := c.ensureDownstreamNamespaceExists(ctx, downstreamNamespace, upstreamObj); err != nil {
		return err
	}

	if eventType == watch.Added {
		if err := c.notifySyncerOwnership(ctx, gvr, upstreamObj); err != nil {
			return err
		}
	}

	downstreamObj := upstreamObj.DeepCopy()
	downstreamObj.SetUID("")
	downstreamObj.SetResourceVersion("")
	downstreamObj.SetNamespace(downstreamNamespace)
	downstreamObj.SetManagedFields(nil)
	downstreamObj.SetClusterName("")
	// Deletion fields are immutable and set by the downstream API server
	downstreamObj.SetDeletionTimestamp(nil)
	downstreamObj.SetDeletionGracePeriodSeconds(nil)
	// Strip owner references, to avoid orphaning by broken references,
	// and make sure cascading deletion is only performed once upstream.
	downstreamObj.SetOwnerReferences(nil)
	// Strip finalizers to avoid the deletion of the downstream resource from being blocked.
	downstreamObj.SetFinalizers(nil)

	// Run name transformations on the downstreamObj.
	transformName(downstreamObj, SyncDown)

	// Run any transformations on the object before we apply it to the downstream cluster.
	if mutator, ok := c.mutators[gvr]; ok {
		if err := mutator(downstreamObj); err != nil {
			return err
		}
	}

	// TODO: wipe things like finalizers, owner-refs and any other life-cycle fields. The life-cycle
	//       should exclusively owned by the syncer. Let's not some Kubernetes magic interfere with it.
	if deletionTimestamp := upstreamObj.GetDeletionTimestamp(); deletionTimestamp != nil || upstreamObj.GetAnnotations()[LocationDeletionAnnotationName(c.pcluster)] != "" {
		// TODO(jmprusi): This should be dropped once we have the virtual syncer workspace support.
		// Check if there's any externalFinalizers, if there are, let's block the deletion of the object as there is an external controller that has
		// set a "soft" finalizer for the given this location.
		if finalizers, ok := c.getLocationFinalizer(upstreamObj); ok {
			klog.Infof("Deletion of resource %s %s/%s from downstream %s|%s/%s is blocked due to an external finalizer: %q", gvr.Resource, upstreamObj.GetNamespace(), upstreamObj.GetName(), upstreamObj.GetClusterName(), upstreamObj.GetNamespace(), upstreamObj.GetName(), finalizers)
			return nil
		}

		if err := c.toClient.Resource(gvr).Namespace(downstreamNamespace).Delete(ctx, downstreamObj.GetName(), metav1.DeleteOptions{}); err != nil {
			if errors.IsNotFound(err) {
				// That's not an error.
				// Just think about removing the finalizer from the KCP location-specific resource:
				if err := c.removeFinalizersAndUpdate(ctx, c.fromClient, gvr, upstreamObj.GetNamespace(), upstreamObj.GetName()); err != nil {
					return err
				}
				return nil
			}
			klog.Infof("Error deleting %s %s/%s from downstream %s|%s/%s: %v", gvr.Resource, upstreamObj.GetNamespace(), upstreamObj.GetName(), upstreamObj.GetClusterName(), upstreamObj.GetNamespace(), upstreamObj.GetName(), err)
			return err
		}
		klog.Infof("Deleted %s %s/%s from downstream %s|%s/%s", gvr.Resource, upstreamObj.GetNamespace(), downstreamObj.GetName(), upstreamObj.GetClusterName(), upstreamObj.GetNamespace(), upstreamObj.GetName())
		return nil
	}

	// Marshalling the unstructured object is good enough as SSA patch
	data, err := json.Marshal(downstreamObj)
	if err != nil {
		return err
	}

	if _, err := c.toClient.Resource(gvr).Namespace(downstreamNamespace).Patch(ctx, downstreamObj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: syncerApplyManager, Force: pointer.Bool(true)}); err != nil {
		klog.Infof("Error upserting %s %s/%s from upstream %s|%s/%s: %v", gvr.Resource, downstreamObj.GetNamespace(), downstreamObj.GetName(), upstreamObj.GetClusterName(), upstreamObj.GetNamespace(), upstreamObj.GetName(), err)
		return err
	}
	klog.Infof("Upserted %s %s/%s from upstream %s|%s/%s", gvr.Resource, downstreamObj.GetNamespace(), downstreamObj.GetName(), upstreamObj.GetClusterName(), upstreamObj.GetNamespace(), upstreamObj.GetName())

	return nil
}
