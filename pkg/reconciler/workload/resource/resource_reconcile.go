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

package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	syncershared "github.com/kcp-dev/kcp/pkg/syncer/shared"
)

// reconcileResource is responsible for setting the cluster for a resource of
// any type, to match the cluster where its namespace is assigned.
func (c *Controller) reconcileResource(ctx context.Context, lclusterName logicalcluster.Name, obj *unstructured.Unstructured, gvr *schema.GroupVersionResource) error {
	logger := logging.WithObject(logging.WithReconciler(klog.Background(), ControllerName), obj).WithValues("groupVersionResource", gvr.String(), "logicalCluster", lclusterName.String())
	logger.V(4).Info("reconciling resource")

	// if the resource is a namespace, let's return early. nothing to do.
	namespaceGVR := &schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	if gvr == namespaceGVR {
		logger.V(5).Info("resource is a namespace; ignoring")
		return nil
	}

	var err error
	var expectedSyncTargetKeys sets.String
	expectedDeletedSynctargetKeys := make(map[string]string)

	namespaceName := obj.GetNamespace()
	// We need to handle namespaced and non-namespaced resources differently, as namespaced resources
	// will get the locations from its namespace, and non-namespaced will get the locations from all the
	// workspace placements.
	if namespaceName != "" {
		logger := logger.WithValues("namespace", namespaceName)
		if namespaceBlocklist.Has(namespaceName) {
			logger.V(4).Info("skipping syncing namespace because it is in the block list")
			return nil
		}

		namespace, err := c.getNamespace(lclusterName, namespaceName)
		if apierrors.IsNotFound(err) {
			// Namespace was deleted; this resource will eventually get deleted too, so ignore
			return nil
		}
		if err != nil {
			return fmt.Errorf("error reconciling resource %s|%s/%s: error getting namespace: %w", lclusterName, namespaceName, obj.GetName(), err)
		}

		expectedSyncTargetKeys = getLocations(namespace.GetLabels(), false)
		expectedDeletedSynctargetKeys = getDeletingLocations(namespace.GetAnnotations())
	} else {
		// We only allow some cluster-wide types of resources.
		if !syncershared.SyncableClusterScopedResources.Has(gvr.String()) {
			logger.V(5).Info("skipping syncing cluster-scoped resource because it is not in the allowed list of syncable cluster-scoped resources", "name", obj.GetName())
			return nil
		}

		logger.Info("reconciling cluster-wide resource", "name", obj.GetName(), "labels", obj.GetLabels())

		// now we need to calculate the synctargets that need to be deleted.
		// we do this by getting the current locations of the resource and
		// comparing against the expected locations.

		expectedSyncTargetKeys, err = c.getValidSyncTargetKeysForWorkspace(logicalcluster.From(obj))
		if err != nil {
			logger.Error(err, "error getting valid sync target keys for workspace")
			return nil
		}

		deletionTimestamp := time.Now().Format(time.RFC3339)
		currentLocations := getLocations(obj.GetLabels(), false)

		for _, location := range currentLocations.Difference(expectedSyncTargetKeys).List() {
			expectedDeletedSynctargetKeys[location] = deletionTimestamp
		}
	}

	var annotationPatch, labelPatch map[string]interface{}

	// If the object DeletionTimestamp is set, we should set all locations deletion timestamps annotations to the same value.
	if obj.GetDeletionTimestamp() != nil {
		annotationPatch = propagateDeletionTimestamp(logger, obj)
	} else {
		// We only need to compute the new placements if the resource is not being deleted.
		annotationPatch, labelPatch = computePlacement(expectedSyncTargetKeys, expectedDeletedSynctargetKeys, obj)
	}

	// clean finalizers from removed syncers
	filteredFinalizers := make([]string, 0, len(obj.GetFinalizers()))
	for _, f := range obj.GetFinalizers() {
		logger := logger.WithValues("finalizer", f)
		if !strings.HasPrefix(f, syncershared.SyncerFinalizerNamePrefix) {
			filteredFinalizers = append(filteredFinalizers, f)
			continue
		}

		syncTargetKey := strings.TrimPrefix(f, syncershared.SyncerFinalizerNamePrefix)
		logger = logger.WithValues("syncTargetKey", syncTargetKey)
		_, found, err := c.getSyncTargetFromKey(syncTargetKey)
		if err != nil {
			logger.Error(err, "error checking if sync target key exists")
			continue
		}
		if !found {
			logger.V(3).Info("SyncTarget under the key was deleted, removing finalizer")
			continue
		}
		logger.V(4).Info("SyncTarget under the key still exists, keeping finalizer")
		filteredFinalizers = append(filteredFinalizers, f)
	}

	// create patch
	if len(labelPatch) == 0 && len(annotationPatch) == 0 && len(filteredFinalizers) == len(obj.GetFinalizers()) {
		logger.V(4).Info("nothing to change for resource")
		return nil
	}

	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"UID":             obj.GetUID(),
			"resourceVersion": obj.GetResourceVersion(),
		},
	}
	if len(labelPatch) > 0 {
		if err := unstructured.SetNestedField(patch, labelPatch, "metadata", "labels"); err != nil {
			logger.Error(err, "unexpected unstructured error")
			return err // should never happen
		}
	}
	if len(annotationPatch) > 0 {
		if err := unstructured.SetNestedField(patch, annotationPatch, "metadata", "annotations"); err != nil {
			logger.Error(err, "unexpected unstructured error")
			return err // should never happen
		}
	}
	if len(filteredFinalizers) != len(obj.GetFinalizers()) {
		if err := unstructured.SetNestedStringSlice(patch, filteredFinalizers, "metadata", "finalizers"); err != nil {
			return err // should never happen
		}
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		logger.Error(err, "unexpected marshal error")
		return err
	}

	logger.WithValues("patch", string(patchBytes)).V(2).Info("patching resource")
	if namespaceName != "" {
		if _, err := c.dynClusterClient.Resource(*gvr).Cluster(lclusterName).Namespace(namespaceName).Patch(ctx, obj.GetName(), types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
			return err
		}
		return nil
	}

	if _, err := c.dynClusterClient.Resource(*gvr).Cluster(lclusterName).Patch(ctx, obj.GetName(), types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return err
	}

	return nil
}

func propagateDeletionTimestamp(logger logr.Logger, obj metav1.Object) map[string]interface{} {
	logger.V(3).Info("resource is being deleted; setting the deletion per locations timestamps")
	objAnnotations := obj.GetAnnotations()
	objLocations := getLocations(obj.GetLabels(), false)
	annotationPatch := make(map[string]interface{})
	for location := range objLocations {
		if val, ok := objAnnotations[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+location]; !ok || val == "" {
			annotationPatch[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+location] = obj.GetDeletionTimestamp().Format(time.RFC3339)
		}
	}
	return annotationPatch
}

// computePlacement computes the patch against annotations and labels. Nil means to remove the key.ResourceStatePending
func computePlacement(expectedSyncTargetKeys sets.String, expectedDeletedSynctargetKeys map[string]string, obj metav1.Object) (annotationPatch map[string]interface{}, labelPatch map[string]interface{}) {
	currentSynctargetKeys := getLocations(obj.GetLabels(), false)
	currentSynctargetKeysDeleting := getDeletingLocations(obj.GetAnnotations())
	if currentSynctargetKeys.Equal(expectedSyncTargetKeys) && reflect.DeepEqual(currentSynctargetKeysDeleting, expectedDeletedSynctargetKeys) {
		// already correctly assigned.
		return
	}

	// create merge patch
	annotationPatch = map[string]interface{}{}
	labelPatch = map[string]interface{}{}

	// unschedule objects from SyncTargets that are no longer expected.
	for _, loc := range currentSynctargetKeys.Difference(expectedSyncTargetKeys).List() {
		// That's an inconsistent state, in case of namespaced resources, it's probably due to the namespace deletion reaching its grace period => let's repair it
		var hasSyncerFinalizer, hasClusterFinalizer bool
		// Check if there's still the syncer or the cluster finalizer.
		for _, finalizer := range obj.GetFinalizers() {
			if finalizer == syncershared.SyncerFinalizerNamePrefix+loc {
				hasSyncerFinalizer = true
			}
		}
		if val, exists := obj.GetAnnotations()[workloadv1alpha1.ClusterFinalizerAnnotationPrefix+loc]; exists && val != "" {
			hasClusterFinalizer = true
		}
		if hasSyncerFinalizer || hasClusterFinalizer {
			if _, found := obj.GetAnnotations()[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+loc]; !found {
				annotationPatch[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+loc] = time.Now().Format(time.RFC3339)
			}
		} else {
			if _, found := obj.GetAnnotations()[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+loc]; found {
				annotationPatch[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+loc] = nil
			}
			labelPatch[workloadv1alpha1.ClusterResourceStateLabelPrefix+loc] = nil
		}
	}

	// sync deletion timestamps if the location is expected to be deleted.
	for _, loc := range expectedSyncTargetKeys.Intersection(currentSynctargetKeys).List() {
		if expectedTimestamp, ok := expectedDeletedSynctargetKeys[loc]; ok {
			if _, ok := currentSynctargetKeysDeleting[loc]; !ok {
				annotationPatch[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+loc] = expectedTimestamp
			}
		} else {
			if _, ok := currentSynctargetKeysDeleting[loc]; ok {
				annotationPatch[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+loc] = nil
			}
		}
	}

	// If the resource is namespaced, set the initial resource state to Sync, otherwise set it to Pending.
	// TODO(jmprusi): ResourceStatePending will be the default state once there is a default coordinators for resources.
	resourceState := workloadv1alpha1.ResourceStateSync
	if obj.GetNamespace() == "" {
		resourceState = workloadv1alpha1.ResourceStatePending
	}

	// set label on unscheduled objects if resource is scheduled and not deleting
	for _, loc := range expectedSyncTargetKeys.Difference(currentSynctargetKeys).List() {
		if _, ok := expectedDeletedSynctargetKeys[loc]; ok {
			continue
		}
		// TODO(sttts): add way to go into pending state first, maybe with a namespace annotation
		labelPatch[workloadv1alpha1.ClusterResourceStateLabelPrefix+loc] = string(resourceState)
	}

	if len(annotationPatch) == 0 {
		annotationPatch = nil
	}
	if len(labelPatch) == 0 {
		labelPatch = nil
	}

	return
}

func (c *Controller) reconcileGVR(gvr schema.GroupVersionResource) error {
	inf, err := c.ddsif.ForResource(gvr)
	if err != nil {
		return err
	}
	if !inf.Informer().HasSynced() {
		return fmt.Errorf("informer for %q is not synced; re-enqueueing", gvr)
	}

	// Update all resources in the namespaces with cluster assignment.
	objs, err := inf.Lister().List(labels.Everything())
	if err != nil {
		return err
	}
	for _, obj := range objs {
		c.enqueueResource(gvr, obj)
	}
	return nil
}
