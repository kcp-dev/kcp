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

package spec

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const (
	syncerApplyManager = "syncer"
)

type mutatorGvrMap map[schema.GroupVersionResource]func(obj *unstructured.Unstructured) error

func deepEqualApartFromStatus(oldUnstrob, newUnstrob *unstructured.Unstructured) bool {
	// TODO(jmprusi): Remove this after switching to virtual workspaces.
	// remove status annotation from oldObj and newObj before comparing
	oldAnnotations, _, err := unstructured.NestedStringMap(oldUnstrob.Object, "metadata", "annotations")
	if err != nil {
		klog.Errorf("failed to get annotations from object: %v", err)
		return false
	}
	for k := range oldAnnotations {
		if strings.HasPrefix(k, workloadv1alpha1.InternalClusterStatusAnnotationPrefix) {
			delete(oldAnnotations, k)
		}
	}

	newAnnotations, _, err := unstructured.NestedStringMap(newUnstrob.Object, "metadata", "annotations")
	if err != nil {
		klog.Errorf("failed to get annotations from object: %v", err)
		return false
	}
	for k := range newAnnotations {
		if strings.HasPrefix(k, workloadv1alpha1.InternalClusterStatusAnnotationPrefix) {
			delete(newAnnotations, k)
		}
	}

	if !equality.Semantic.DeepEqual(oldAnnotations, newAnnotations) {
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

func (c *Controller) process(ctx context.Context, gvr schema.GroupVersionResource, key string) error {
	klog.V(3).InfoS("Processing", "gvr", gvr, "key", key)

	// from upstream
	upstreamNamespace, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("Invalid key %q: %v", key, err)
		return nil
	}
	clusterName, name := clusters.SplitClusterAwareKey(clusterAwareName)

	namespaceGvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	desiredNSLocator := shared.NewNamespaceLocator(clusterName, c.syncTargetWorkspace, c.syncTargetUID, c.syncTargetName, upstreamNamespace)
	jsonNSLocator, err := json.Marshal(desiredNSLocator)
	if err != nil {
		return err
	}
	downstreamNamespaces, err := c.downstreamInformers.ForResource(namespaceGvr).Informer().GetIndexer().ByIndex(byNamespaceLocatorIndexName, string(jsonNSLocator))
	if err != nil {
		return err
	}

	var downstreamNamespace string
	if len(downstreamNamespaces) == 1 {
		namespace := downstreamNamespaces[0].(*unstructured.Unstructured)
		klog.V(4).Infof("Found downstream namespace %s for upstream namespace %s", namespace.GetName(), upstreamNamespace)
		downstreamNamespace = namespace.GetName()
	} else if len(downstreamNamespaces) > 1 {
		// This should never happen unless there's some namespace collision.
		var namespacesCollisions []string
		for _, namespace := range downstreamNamespaces {
			namespacesCollisions = append(namespacesCollisions, namespace.(*unstructured.Unstructured).GetName())
		}
		return fmt.Errorf("(namespace collision) found multiple downstream namespaces: %s for upstream namespace %s|%s", strings.Join(namespacesCollisions, ","), clusterName, upstreamNamespace)
	} else {
		klog.V(4).Infof("No downstream namespaces found for %s", key)
		downstreamNamespace, err = shared.PhysicalClusterNamespaceName(desiredNSLocator)
		if err != nil {
			klog.Errorf("Error hashing namespace %s|%s: %v", clusterName, upstreamNamespace, err)
			return nil
		}
	}

	// get the upstream object
	obj, exists, err := c.upstreamInformers.ForResource(gvr).Informer().GetIndexer().GetByKey(key)
	if err != nil {
		return err
	}
	if !exists {
		// deleted upstream => delete downstream
		klog.Infof("Deleting downstream GVR %q object %s/%s for upstream cluster %q", gvr.String(), upstreamNamespace, name, clusterName)
		if err := c.downstreamClient.Resource(gvr).Namespace(downstreamNamespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	// upsert downstream
	upstreamObj, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("object to synchronize is expected to be Unstructured, but is %T", obj)
	}

	if err := c.ensureDownstreamNamespaceExists(ctx, downstreamNamespace, upstreamObj); err != nil {
		return err
	}

	if added, err := c.ensureSyncerFinalizer(ctx, gvr, upstreamObj); added {
		// The successful update of the upstream resource finalizer will trigger a new reconcile
		return nil
	} else if err != nil {
		return err
	}

	return c.applyToDownstream(ctx, gvr, downstreamNamespace, upstreamObj)
}

// TODO: This function is there as a quick and dirty implementation of namespace creation.
//
//	In fact We should also be getting notifications about namespaces created upstream and be creating downstream equivalents.
func (c *Controller) ensureDownstreamNamespaceExists(ctx context.Context, downstreamNamespace string, upstreamObj *unstructured.Unstructured) error {
	namespaces := c.downstreamClient.Resource(schema.GroupVersionResource{
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
	upstreamLogicalCluster := logicalcluster.From(upstreamObj)
	desiredNSLocator := shared.NewNamespaceLocator(upstreamLogicalCluster, c.syncTargetWorkspace, c.syncTargetUID, c.syncTargetName, upstreamObj.GetNamespace())
	b, err := json.Marshal(desiredNSLocator)
	if err != nil {
		return err
	}
	newNamespace.SetAnnotations(map[string]string{
		shared.NamespaceLocatorAnnotation: string(b),
	})

	if upstreamObj.GetLabels() != nil {
		newNamespace.SetLabels(map[string]string{
			// TODO: this should be set once at syncer startup and propagated around everywhere.
			workloadv1alpha1.InternalDownstreamClusterLabel: c.syncTargetKey,
		})
	}

	// Check if the namespace already exists, if not create it.
	namespace, err := c.downstreamInformers.ForResource(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}).Lister().Get(newNamespace.GetName())
	if err != nil && apierrors.IsNotFound(err) {
		if _, err := namespaces.Create(ctx, newNamespace, metav1.CreateOptions{}); err != nil {
			return err
		}
		klog.Infof("Created downstream namespace %s for upstream namespace %s|%s", newNamespace.GetName(), desiredNSLocator.Workspace, desiredNSLocator.Namespace)
		return nil
	} else if err != nil {
		return err
	}

	// The namespace exists, so check if it has the correct namespace locator.
	unstrNamespace := namespace.(*unstructured.Unstructured)
	nsLocator, exists, err := shared.LocatorFromAnnotations(unstrNamespace.GetAnnotations())
	if err != nil {
		return fmt.Errorf("(possible namespace collision) namespace %s already exists, but found an error when trying to decode the annotation: %w", newNamespace.GetName(), err)
	}
	if !exists {
		return fmt.Errorf("(namespace collision) namespace %s has no namespace locator", unstrNamespace.GetName())
	}
	if !reflect.DeepEqual(desiredNSLocator, *nsLocator) {
		return fmt.Errorf("(namespace collision) namespace %s already exists, but has a different namespace locator annotation: %+v vs %+v", newNamespace.GetName(), nsLocator, desiredNSLocator)
	}

	return nil
}

func (c *Controller) ensureSyncerFinalizer(ctx context.Context, gvr schema.GroupVersionResource, upstreamObj *unstructured.Unstructured) (bool, error) {
	upstreamFinalizers := upstreamObj.GetFinalizers()
	hasFinalizer := false
	for _, finalizer := range upstreamFinalizers {
		if finalizer == shared.SyncerFinalizerNamePrefix+c.syncTargetKey {
			hasFinalizer = true
		}
	}

	// TODO(davidfestal): When using syncer virtual workspace we would check the DeletionTimestamp on the upstream object, instead of the DeletionTimestamp annotation,
	//                as the virtual workspace will set the the deletionTimestamp() on the location view by a transformation.
	intendedToBeRemovedFromLocation := upstreamObj.GetAnnotations()[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+c.syncTargetKey] != ""

	// TODO(davidfestal): When using syncer virtual workspace this condition would not be necessary anymore, since directly tested on the virtual workspace side.
	stillOwnedByExternalActorForLocation := upstreamObj.GetAnnotations()[workloadv1alpha1.ClusterFinalizerAnnotationPrefix+c.syncTargetKey] != ""

	if !hasFinalizer && (!intendedToBeRemovedFromLocation || stillOwnedByExternalActorForLocation) {
		upstreamObjCopy := upstreamObj.DeepCopy()
		name := upstreamObjCopy.GetName()
		namespace := upstreamObjCopy.GetNamespace()
		logicalCluster := logicalcluster.From(upstreamObjCopy)

		upstreamFinalizers = append(upstreamFinalizers, shared.SyncerFinalizerNamePrefix+c.syncTargetKey)
		upstreamObjCopy.SetFinalizers(upstreamFinalizers)
		if _, err := c.upstreamClient.Cluster(logicalCluster).Resource(gvr).Namespace(namespace).Update(ctx, upstreamObjCopy, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("Failed adding finalizer upstream on resource %s|%s/%s: %v", logicalCluster, namespace, name, err)
			return false, err
		}
		klog.Infof("Updated resource %s|%s/%s with syncer finalizer upstream", logicalCluster, namespace, name)
		return true, nil
	}

	return false, nil
}

func (c *Controller) applyToDownstream(ctx context.Context, gvr schema.GroupVersionResource, downstreamNamespace string, upstreamObj *unstructured.Unstructured) error {
	upstreamObjLogicalCluster := logicalcluster.From(upstreamObj)
	downstreamObj := upstreamObj.DeepCopy()

	// Run name transformations on the downstreamObj.
	transformedName := getTransformedName(downstreamObj)

	// TODO(jmprusi): When using syncer virtual workspace we would check the DeletionTimestamp on the upstream object, instead of the DeletionTimestamp annotation,
	//                as the virtual workspace will set the the deletionTimestamp() on the location view by a transformation.
	intendedToBeRemovedFromLocation := upstreamObj.GetAnnotations()[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+c.syncTargetKey] != ""

	// TODO(jmprusi): When using syncer virtual workspace this condition would not be necessary anymore, since directly tested on the virtual workspace side.
	stillOwnedByExternalActorForLocation := upstreamObj.GetAnnotations()[workloadv1alpha1.ClusterFinalizerAnnotationPrefix+c.syncTargetKey] != ""

	klog.V(4).Infof("Upstream object %s|%s/%s is intended to be removed %t", upstreamObjLogicalCluster, upstreamObj.GetNamespace(), upstreamObj.GetName(), intendedToBeRemovedFromLocation, stillOwnedByExternalActorForLocation)
	if intendedToBeRemovedFromLocation && !stillOwnedByExternalActorForLocation {
		if err := c.downstreamClient.Resource(gvr).Namespace(downstreamNamespace).Delete(ctx, transformedName, metav1.DeleteOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				// That's not an error.
				// Just think about removing the finalizer from the KCP location-specific resource:
				if err := shared.EnsureUpstreamFinalizerRemoved(ctx, gvr, c.upstreamInformers, c.upstreamClient, upstreamObj.GetNamespace(), c.syncTargetKey, upstreamObjLogicalCluster, upstreamObj.GetName()); err != nil {
					return err
				}
				return nil
			}
			klog.Errorf("Error deleting %s %s/%s from downstream %s|%s/%s: %v", gvr.Resource, upstreamObj.GetNamespace(), upstreamObj.GetName(), logicalcluster.From(upstreamObj), downstreamNamespace, downstreamObj.GetName(), err)
			return err
		}
		klog.V(2).Infof("Deleted %s %s/%s from downstream %s|%s/%s", gvr.Resource, upstreamObj.GetNamespace(), downstreamObj.GetName(), logicalcluster.From(upstreamObj), downstreamNamespace, downstreamObj.GetName())
		return nil
	}

	// Run any transformations on the object before we apply it to the downstream cluster.
	if mutator, ok := c.mutators[gvr]; ok {
		if err := mutator(downstreamObj); err != nil {
			return err
		}
	}

	downstreamObj.SetName(transformedName)
	downstreamObj.SetUID("")
	downstreamObj.SetResourceVersion("")
	downstreamObj.SetNamespace(downstreamNamespace)
	downstreamObj.SetManagedFields(nil)

	// Strip cluster name annotation
	downstreamAnnotations := downstreamObj.GetAnnotations()
	delete(downstreamAnnotations, logicalcluster.AnnotationKey)
	// If we're left with 0 annotations, nil out the map so it's not included in the patch
	if len(downstreamAnnotations) == 0 {
		downstreamAnnotations = nil
	}
	downstreamObj.SetAnnotations(downstreamAnnotations)

	// Deletion fields are immutable and set by the downstream API server
	downstreamObj.SetDeletionTimestamp(nil)
	downstreamObj.SetDeletionGracePeriodSeconds(nil)
	// Strip owner references, to avoid orphaning by broken references,
	// and make sure cascading deletion is only performed once upstream.
	downstreamObj.SetOwnerReferences(nil)
	// Strip finalizers to avoid the deletion of the downstream resource from being blocked.
	downstreamObj.SetFinalizers(nil)

	// replace upstream state label with downstream cluster label. We don't want to leak upstream state machine
	// state to downstream, and also we don't need downstream updates every time the upstream state machine changes.
	labels := downstreamObj.GetLabels()
	delete(labels, workloadv1alpha1.ClusterResourceStateLabelPrefix+c.syncTargetKey)
	labels[workloadv1alpha1.InternalDownstreamClusterLabel] = c.syncTargetKey
	downstreamObj.SetLabels(labels)

	if c.advancedSchedulingEnabled {
		specDiffPatch := upstreamObj.GetAnnotations()[workloadv1alpha1.ClusterSpecDiffAnnotationPrefix+c.syncTargetKey]
		if specDiffPatch != "" {
			upstreamSpec, specExists, err := unstructured.NestedFieldCopy(upstreamObj.UnstructuredContent(), "spec")
			if err != nil {
				return err
			}
			if specExists {
				// TODO(jmprusi): Surface those errors to the user.
				patch, err := jsonpatch.DecodePatch([]byte(specDiffPatch))
				if err != nil {
					klog.Errorf("Failed to decode spec diff patch: %v", err)
					return err
				}
				upstreamSpecJSON, err := json.Marshal(upstreamSpec)
				if err != nil {
					return err
				}
				patchedUpstreamSpecJSON, err := patch.Apply(upstreamSpecJSON)
				if err != nil {
					return err
				}
				var newSpec map[string]interface{}
				if err := json.Unmarshal(patchedUpstreamSpecJSON, &newSpec); err != nil {
					return err
				}
				if err := unstructured.SetNestedMap(downstreamObj.UnstructuredContent(), newSpec, "spec"); err != nil {
					return err
				}
			}
		}
	}

	// Marshalling the unstructured object is good enough as SSA patch
	data, err := json.Marshal(downstreamObj)
	if err != nil {
		return err
	}

	if _, err := c.downstreamClient.Resource(gvr).Namespace(downstreamNamespace).Patch(ctx, downstreamObj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: syncerApplyManager, Force: pointer.Bool(true)}); err != nil {
		klog.Errorf("Error upserting %s %s/%s from upstream %s|%s/%s: %v", gvr.Resource, downstreamObj.GetNamespace(), downstreamObj.GetName(), logicalcluster.From(upstreamObj), upstreamObj.GetNamespace(), upstreamObj.GetName(), err)
		return err
	}
	klog.Infof("Upserted %s %s/%s from upstream %s|%s/%s", gvr.Resource, downstreamObj.GetNamespace(), downstreamObj.GetName(), logicalcluster.From(upstreamObj), upstreamObj.GetNamespace(), upstreamObj.GetName())

	return nil
}

// getTransformedName returns the desired object name.
func getTransformedName(syncedObject *unstructured.Unstructured) string {
	configMapGVK := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}
	secretGVK := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"}

	if syncedObject.GroupVersionKind() == configMapGVK && syncedObject.GetName() == "kube-root-ca.crt" {
		return "kcp-root-ca.crt"
	}
	// Only rename the default-token-* secrets that are owned by the default SA.
	if syncedObject.GroupVersionKind() == secretGVK && strings.HasPrefix(syncedObject.GetName(), "default-token-") {
		if saName, ok := syncedObject.GetAnnotations()[corev1.ServiceAccountNameKey]; ok && saName == "default" {
			return "kcp-" + syncedObject.GetName()
		}
	}
	return syncedObject.GetName()
}
