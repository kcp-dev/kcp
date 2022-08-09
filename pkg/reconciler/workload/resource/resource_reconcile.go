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
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	syncershared "github.com/kcp-dev/kcp/pkg/syncer/shared"
)

// reconcileResource is responsible for setting the cluster for a resource of
// any type, to match the cluster where its namespace is assigned.
func (c *Controller) reconcileResource(ctx context.Context, lclusterName logicalcluster.Name, obj *unstructured.Unstructured, gvr *schema.GroupVersionResource) error {
	logger := logging.WithObject(logging.WithReconciler(klog.Background(), controllerName), obj).WithValues("groupVersionResource", gvr.String(), "logicalCluster", lclusterName.String())
	logger.V(4).Info("reconciling resource")

	// If the resource is not namespaced (incl if the resource is itself a
	// namespace), ignore it.
	if obj.GetNamespace() == "" {
		logger.V(4).Info("resource had no namespace; ignoring")
		return nil
	}

	if namespaceBlocklist.Has(obj.GetNamespace()) {
		logger.V(4).Info("skipping syncing namespace")
		return nil
	}

	// Align the resource's assigned cluster with the namespace's assigned
	// cluster.
	// First, get the namespace object (from the cached lister).
	ns, err := c.namespaceLister.Get(clusters.ToClusterAwareKey(lclusterName, obj.GetNamespace()))
	if apierrors.IsNotFound(err) {
		// Namespace was deleted; this resource will eventually get deleted too, so ignore
		return nil
	}
	if err != nil {
		return fmt.Errorf("error reconciling resource %s|%s/%s: error getting namespace: %w", lclusterName, obj.GetNamespace(), obj.GetName(), err)
	}

	annotationPatch, labelPatch := computePlacement(ns, obj)

	// If the object DeletionTimestamp is set, we should set all locations deletion timestamps annotations to the same value.
	if obj.GetDeletionTimestamp() != nil {
		annotationPatch = propagateDeletionTimestamp(logger, obj, annotationPatch)
	}

	// clean finalizers from removed syncers
	filteredFinalizers := make([]string, 0, len(obj.GetFinalizers()))
	for _, f := range obj.GetFinalizers() {
		logger = logger.WithValues("finalizer", f)
		if !strings.HasPrefix(f, syncershared.SyncerFinalizerNamePrefix) {
			filteredFinalizers = append(filteredFinalizers, f)
			continue
		}

		syncTargetKey := strings.TrimPrefix(f, syncershared.SyncerFinalizerNamePrefix)
		logger = logger.WithValues("syncTargetKey", syncTargetKey)
		objs, err := c.syncTargetIndexer.ByIndex(indexers.SyncTargetsBySyncTargetKey, syncTargetKey)
		if err != nil {
			logger.Error(err, "error getting SyncTarget via index")
			continue
		}
		if len(objs) == 0 {
			logger.V(3).Info("SyncTarget under the key was deleted, removing finalizer")
			continue
		}
		aCluster := objs[0].(*workloadv1alpha1.SyncTarget)
		logging.WithObject(logger, aCluster).V(5).Info("keeping finalizer because of SyncTarget")
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
	if _, err := c.dynClusterClient.Resource(*gvr).Namespace(ns.Name).
		Patch(logicalcluster.WithCluster(ctx, lclusterName), obj.GetName(), types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return err
	}

	return nil
}

func propagateDeletionTimestamp(logger logr.Logger, obj metav1.Object, annotationPatch map[string]interface{}) map[string]interface{} {
	logger.V(3).Info("resource is being deleted; setting the deletion per locations timestamps")
	objAnnotations := obj.GetAnnotations()
	objLocations, _ := locations(objAnnotations, obj.GetLabels(), false)
	if annotationPatch == nil {
		annotationPatch = make(map[string]interface{})
	}
	for location := range objLocations {
		if val, ok := objAnnotations[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+location]; !ok || val == "" {
			annotationPatch[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+location] = obj.GetDeletionTimestamp().Format(time.RFC3339)
		}
	}
	return annotationPatch
}

// computePlacement computes the patch against annotations and labels. Nil means to remove the key.
func computePlacement(ns *corev1.Namespace, obj metav1.Object) (annotationPatch map[string]interface{}, labelPatch map[string]interface{}) {
	nsLocations, nsDeleting := locations(ns.Annotations, ns.Labels, true)
	objLocations, objDeleting := locations(obj.GetAnnotations(), obj.GetLabels(), false)
	if objLocations.Equal(nsLocations) && objDeleting.Equal(nsDeleting) {
		// already correctly assigned.
		return
	}

	// create merge patch
	annotationPatch = map[string]interface{}{}
	labelPatch = map[string]interface{}{}
	for _, loc := range objLocations.Difference(nsLocations).List() {
		// location was removed from namespace, but is still on the object
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
				labelPatch[workloadv1alpha1.ClusterResourceStateLabelPrefix+loc] = nil
			}
		}
	}
	for _, loc := range nsLocations.Intersection(nsLocations).List() {
		if nsTimestamp, found := ns.Annotations[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+loc]; found && validRFC3339(nsTimestamp) {
			objTimestamp, found := obj.GetAnnotations()[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+loc]
			if !found || !validRFC3339(objTimestamp) {
				annotationPatch[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+loc] = nsTimestamp
			}
		}
	}
	for _, loc := range nsLocations.Difference(objLocations).List() {
		// location was missing on the object
		// TODO(sttts): add way to go into pending state first, maybe with a namespace annotation
		labelPatch[workloadv1alpha1.ClusterResourceStateLabelPrefix+loc] = string(workloadv1alpha1.ResourceStateSync)
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

func validRFC3339(ts string) bool {
	_, err := time.Parse(time.RFC3339, ts)
	return err == nil
}
