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
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	. "github.com/kcp-dev/kcp/tmc/pkg/logging"
)

const (
	syncerApplyManager = "syncer"
)

type mutatorGvrMap map[schema.GroupVersionResource]func(obj *unstructured.Unstructured) error

func deepEqualApartFromStatus(logger logr.Logger, oldUnstrob, newUnstrob *unstructured.Unstructured) bool {
	// TODO(jmprusi): Remove this after switching to virtual workspaces.
	// remove status annotation from oldObj and newObj before comparing
	oldAnnotations, _, err := unstructured.NestedStringMap(oldUnstrob.Object, "metadata", "annotations")
	if err != nil {
		logger.Error(err, "failed to get annotations from object")
		return false
	}
	for k := range oldAnnotations {
		if strings.HasPrefix(k, workloadv1alpha1.InternalClusterStatusAnnotationPrefix) {
			delete(oldAnnotations, k)
		}
	}

	newAnnotations, _, err := unstructured.NestedStringMap(newUnstrob.Object, "metadata", "annotations")
	if err != nil {
		logger.Error(err, "failed to get annotations from object")
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

func (c *Controller) process(ctx context.Context, gvr schema.GroupVersionResource, key string) (retryAfter *time.Duration, err error) {
	logger := klog.FromContext(ctx)

	// from upstream
	clusterName, upstreamNamespace, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "Invalid key")
		return nil, nil
	}
	logger = logger.WithValues(logging.WorkspaceKey, clusterName, logging.NamespaceKey, upstreamNamespace, logging.NameKey, name)

	desiredNSLocator := shared.NewNamespaceLocator(clusterName, c.syncTargetClusterName, c.syncTargetUID, c.syncTargetName, upstreamNamespace)
	jsonNSLocator, err := json.Marshal(desiredNSLocator)
	if err != nil {
		return nil, err
	}

	var downstreamNamespace string
	// Only look for the downstream namespace if the resource is namespaced, avoid in case of cluster-scoped.
	if upstreamNamespace != "" {
		downstreamNamespaces, err := c.downstreamNSInformer.Informer().GetIndexer().ByIndex(byNamespaceLocatorIndexName, string(jsonNSLocator))
		if err != nil {
			return nil, err
		}

		if len(downstreamNamespaces) == 1 {
			namespace := downstreamNamespaces[0].(*unstructured.Unstructured)
			logger.WithValues(DownstreamName, namespace.GetName()).V(4).Info("Found downstream namespace for upstream namespace")
			downstreamNamespace = namespace.GetName()
		} else if len(downstreamNamespaces) > 1 {
			// This should never happen unless there's some namespace collision.
			var namespacesCollisions []string
			for _, namespace := range downstreamNamespaces {
				namespacesCollisions = append(namespacesCollisions, namespace.(*unstructured.Unstructured).GetName())
			}
			return nil, fmt.Errorf("(namespace collision) found multiple downstream namespaces: %s for upstream namespace %s|%s", strings.Join(namespacesCollisions, ","), clusterName, upstreamNamespace)
		} else {
			logger.V(4).Info("No downstream namespaces found")
			downstreamNamespace, err = shared.PhysicalClusterNamespaceName(desiredNSLocator)
			if err != nil {
				logger.Error(err, "Error hashing namespace")
				return nil, nil
			}
		}
	}
	logger = logger.WithValues(DownstreamNamespace, downstreamNamespace)

	// get the upstream object
	syncerInformer, ok := c.syncerInformers.InformerForResource(gvr)
	if !ok {
		return nil, nil
	}

	obj, exists, err := syncerInformer.UpstreamInformer.Informer().GetIndexer().GetByKey(kcpcache.ToClusterAwareKey(clusterName.String(), upstreamNamespace, name))
	if err != nil {
		return nil, err
	}
	if !exists {
		// deleted upstream => delete downstream
		logger.Info("Deleting downstream object for upstream object")
		if downstreamNamespace != "" {
			err = c.downstreamClient.Resource(gvr).Namespace(downstreamNamespace).Delete(ctx, name, metav1.DeleteOptions{})
		} else {
			err = c.downstreamClient.Resource(gvr).Delete(ctx, name, metav1.DeleteOptions{})
		}
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, err
		}
		// If the resource is namespaced, let's plan the cleanup of it's namespace.
		if downstreamNamespace != "" {
			c.downstreamNSCleaner.PlanCleaning(downstreamNamespace)
		}
		return nil, nil
	}

	// upsert downstream
	upstreamObj, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("object to synchronize is expected to be Unstructured, but is %T", obj)
	}

	if downstreamNamespace != "" {
		if err := c.ensureDownstreamNamespaceExists(ctx, downstreamNamespace, upstreamObj); err != nil {
			return nil, err
		}
	} else {
		// In cluser-wide resources we also need to check for possible collisions, as the resource could exist in the pcluster but now owned by this workspace.
		// TODO(jmprusi): We should indicate the collision somehow (condition/annotation?) to avoid retrying the resource over and over.
		if err := c.clusterWideCollisionCheck(ctx, gvr, upstreamObj); err != nil {
			return nil, err
		}
	}

	deploymentGVR := schema.GroupVersionResource{
		Group:    "apps",
		Version:  "v1",
		Resource: "deployments",
	}

	// Make sure the DNS nameserver for the current workspace is up and running
	if dnsUpAndReady, err := c.dnsProcessor.EnsureDNSUpAndReady(ctx, clusterName); err != nil {
		logger.Error(err, "failed to check DNS nameserver is up and running (retrying)")
		return nil, err
	} else if !dnsUpAndReady && gvr == deploymentGVR {
		logger.Info("DNS Server is not ready for the deployment resource to sync: requeue the deployment in 500ms")
		retryDelay := 500 * time.Millisecond
		return &retryDelay, nil
	}

	if added, err := c.ensureSyncerFinalizer(ctx, gvr, upstreamObj); added {
		// The successful update of the upstream resource finalizer will trigger a new reconcile
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	return nil, c.applyToDownstream(ctx, gvr, downstreamNamespace, upstreamObj)
}

// TODO: This function is there as a quick and dirty implementation of namespace creation.
//
//	In fact We should also be getting notifications about namespaces created upstream and be creating downstream equivalents.
func (c *Controller) ensureDownstreamNamespaceExists(ctx context.Context, downstreamNamespace string, upstreamObj *unstructured.Unstructured) error {
	logger := klog.FromContext(ctx)

	// If the namespace was marked for deletion, let's cancel it, as we expect to use it.
	c.downstreamNSCleaner.CancelCleaning(downstreamNamespace)

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
	desiredNSLocator := shared.NewNamespaceLocator(upstreamLogicalCluster, c.syncTargetClusterName, c.syncTargetUID, c.syncTargetName, upstreamObj.GetNamespace())
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
	namespace, err := c.downstreamNSInformer.Lister().Get(newNamespace.GetName())
	if apierrors.IsNotFound(err) {
		_, err = namespaces.Create(ctx, newNamespace, metav1.CreateOptions{})
		if err == nil {
			logger.Info("Created downstream namespace for upstream namespace")
			return nil
		}
	}
	if apierrors.IsAlreadyExists(err) {
		namespace, err = namespaces.Get(ctx, newNamespace.GetName(), metav1.GetOptions{})
	}
	if err != nil {
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

// TODO(jmprusi): merge with ensureDownstreamNamespaceExists and make it more generic
func (c *Controller) clusterWideCollisionCheck(ctx context.Context, gvr schema.GroupVersionResource, upstreamObj *unstructured.Unstructured) error {
	// Check if the resource already exists, if so check if it has the correct namespace locator.
	syncerInformer, ok := c.syncerInformers.InformerForResource(gvr)
	if !ok {
		return nil
	}
	resource, err := syncerInformer.DownstreamInformer.Lister().Get(upstreamObj.GetName())
	if err != nil && apierrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	// The resource exists, so check if it has the correct namespace locator.
	unstrResource := resource.(*unstructured.Unstructured)
	nsLocator, exists, err := shared.LocatorFromAnnotations(unstrResource.GetAnnotations())
	if err != nil {
		return fmt.Errorf("(possible cluster-wide resource collision) resource %s already exists, but found an error when trying to decode the annotation: %w", unstrResource.GetName(), err)
	}
	if !exists {
		return fmt.Errorf("(cluster-wide resource collision) resource %s has no namespace locator", unstrResource.GetName())
	}
	if nsLocator.ClusterName != c.syncTargetClusterName {
		return fmt.Errorf("(cluster-wide resource collision) resource %s already exists, but has a different namespace locator annotation: %+v", unstrResource.GetName(), nsLocator)
	}

	return nil
}

func (c *Controller) ensureSyncerFinalizer(ctx context.Context, gvr schema.GroupVersionResource, upstreamObj *unstructured.Unstructured) (bool, error) {
	logger := klog.FromContext(ctx)

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
		namespace := upstreamObjCopy.GetNamespace()
		clusterName := logicalcluster.From(upstreamObjCopy)

		upstreamFinalizers = append(upstreamFinalizers, shared.SyncerFinalizerNamePrefix+c.syncTargetKey)
		upstreamObjCopy.SetFinalizers(upstreamFinalizers)
		if _, err := c.upstreamClient.Cluster(clusterName.Path()).Resource(gvr).Namespace(namespace).Update(ctx, upstreamObjCopy, metav1.UpdateOptions{}); err != nil {
			logger.Error(err, "Failed adding finalizer on upstream upstreamresource")
			return false, err
		}
		logger.Info("Updated upstream resource with syncer finalizer")
		return true, nil
	}

	return false, nil
}

func (c *Controller) applyToDownstream(ctx context.Context, gvr schema.GroupVersionResource, downstreamNamespace string, upstreamObj *unstructured.Unstructured) error {
	logger := klog.FromContext(ctx)

	upstreamObjLogicalCluster := logicalcluster.From(upstreamObj)
	downstreamObj := upstreamObj.DeepCopy()

	// Run name transformations on the downstreamObj.
	transformedName := getTransformedName(downstreamObj)

	// TODO(jmprusi): When using syncer virtual workspace we would check the DeletionTimestamp on the upstream object, instead of the DeletionTimestamp annotation,
	//                as the virtual workspace will set the the deletionTimestamp() on the location view by a transformation.
	intendedToBeRemovedFromLocation := upstreamObj.GetAnnotations()[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+c.syncTargetKey] != ""

	// TODO(jmprusi): When using syncer virtual workspace this condition would not be necessary anymore, since directly tested on the virtual workspace side.
	stillOwnedByExternalActorForLocation := upstreamObj.GetAnnotations()[workloadv1alpha1.ClusterFinalizerAnnotationPrefix+c.syncTargetKey] != ""

	syncerInformer, ok := c.syncerInformers.InformerForResource(gvr)
	if !ok {
		return nil
	}

	logger = logger.WithValues(DownstreamName, transformedName)
	ctx = klog.NewContext(ctx, logger)

	logger.V(4).Info("Upstream object is intended to be removed", "intendedToBeRemovedFromLocation", intendedToBeRemovedFromLocation, "stillOwnedByExternalActorForLocation", stillOwnedByExternalActorForLocation)
	if intendedToBeRemovedFromLocation && !stillOwnedByExternalActorForLocation {
		var err error
		if downstreamNamespace != "" {
			err = c.downstreamClient.Resource(gvr).Namespace(downstreamNamespace).Delete(ctx, transformedName, metav1.DeleteOptions{})
		} else {
			err = c.downstreamClient.Resource(gvr).Delete(ctx, transformedName, metav1.DeleteOptions{})
		}
		if err != nil {
			if apierrors.IsNotFound(err) {
				// That's not an error.
				// Just think about removing the finalizer from the KCP location-specific resource:
				if err := shared.EnsureUpstreamFinalizerRemoved(ctx, gvr, syncerInformer.UpstreamInformer, c.upstreamClient, upstreamObj.GetNamespace(), c.syncTargetKey, upstreamObjLogicalCluster, upstreamObj.GetName()); err != nil {
					return err
				}
				return nil
			}
			logger.Error(err, "Error deleting upstream resource from downstream")
			return err
		}
		if downstreamNamespace != "" {
			c.downstreamNSCleaner.PlanCleaning(downstreamNamespace)
		}
		logger.V(2).Info("Deleted upstream resource from downstream")
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
	//TODO(jmprusi): To be removed when switching to the syncer Virtual Workspace transformations.
	delete(downstreamAnnotations, workloadv1alpha1.InternalClusterStatusAnnotationPrefix+c.syncTargetKey)
	// If the resource is cluster-scoped, we need to add the namespaceLocator annotation to get be able to
	// find out the upstream resource from the downstream resource.
	if downstreamNamespace == "" {
		namespaceLocator := shared.NewNamespaceLocator(upstreamObjLogicalCluster, c.syncTargetClusterName, c.syncTargetUID, c.syncTargetName, "")
		namespaceLocatorJSONBytes, err := json.Marshal(namespaceLocator)
		if err != nil {
			return err
		}
		downstreamAnnotations[shared.NamespaceLocatorAnnotation] = string(namespaceLocatorJSONBytes)
	}

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
					logger.Error(err, "Failed to decode spec diff patch")
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

	// Check if the resource is cluster-wide or namespaced and patch it appropriately.
	if downstreamNamespace != "" {
		_, err = c.downstreamClient.Resource(gvr).Namespace(downstreamNamespace).Patch(ctx, downstreamObj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: syncerApplyManager, Force: pointer.Bool(true)})
	} else {
		_, err = c.downstreamClient.Resource(gvr).Patch(ctx, downstreamObj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: syncerApplyManager, Force: pointer.Bool(true)})
	}

	if err != nil {
		logger.Error(err, "Error upserting upstream resource to downstream")
		return err
	}
	logger.Info("Upserted upstream resource to downstream")

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
