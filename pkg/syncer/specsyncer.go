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
	"net/url"
	"strings"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	specmutators "github.com/kcp-dev/kcp/pkg/syncer/spec/mutators"
)

const (
	syncerApplyManager = "syncer"
)

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

const specSyncerAgent = "kcp#spec-syncer/v0.0.0"

func NewSpecSyncer(gvrs []string, kcpClusterName logicalcluster.Name, pclusterID string, upstreamURL *url.URL, advancedSchedulingEnabled bool,
	upstreamClient, downstreamClient dynamic.Interface, upstreamInformers, downstreamInformers dynamicinformer.DynamicSharedInformerFactory) (*Controller, error) {

	deploymentMutator := specmutators.NewDeploymentMutator(upstreamURL)
	secretMutator := specmutators.NewSecretMutator()

	return New(kcpClusterName, pclusterID, upstreamClient, downstreamClient, upstreamInformers, SyncDown, gvrs, mutatorGvrMap{
		deploymentMutator.GVR(): deploymentMutator.Mutate,
		secretMutator.GVR():     secretMutator.Mutate,
	}, advancedSchedulingEnabled)
}

func (c *Controller) deleteFromDownstream(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	// TODO: get UID of just-deleted object and pass it as a precondition on this delete.
	// This would avoid races where an object is deleted and another object with the same name is created immediately after.

	if err := c.toClient.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
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
			workloadv1alpha1.InternalClusterResourceStateLabelPrefix + c.workloadClusterName: string(workloadv1alpha1.ResourceStateSync),
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

func (c *Controller) ensureSyncerFinalizer(ctx context.Context, gvr schema.GroupVersionResource, upstreamObj *unstructured.Unstructured) error {
	upstreamFinalizers := upstreamObj.GetFinalizers()
	hasFinalizer := false
	for _, finalizer := range upstreamFinalizers {
		if finalizer == syncerFinalizerNamePrefix+c.workloadClusterName {
			hasFinalizer = true
		}
	}
	if !hasFinalizer {
		upstreamObjCopy := upstreamObj.DeepCopy()
		name := upstreamObjCopy.GetName()
		namespace := upstreamObjCopy.GetNamespace()

		upstreamFinalizers = append(upstreamFinalizers, syncerFinalizerNamePrefix+c.workloadClusterName)
		upstreamObjCopy.SetFinalizers(upstreamFinalizers)
		if _, err := c.fromClient.Resource(gvr).Namespace(namespace).Update(ctx, upstreamObjCopy, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("Failed adding finalizer upstream on resource %s|%s/%s: %v", c.upstreamClusterName, namespace, name, err)
			return err
		}
		klog.Infof("Updated resource %s|%s/%s with syncer finalizer upstream", c.upstreamClusterName, namespace, name)
	}

	return nil
}

func (c *Controller) applyToDownstream(ctx context.Context, gvr schema.GroupVersionResource, downstreamNamespace string, upstreamObj *unstructured.Unstructured) error {
	if err := c.ensureDownstreamNamespaceExists(ctx, downstreamNamespace, upstreamObj); err != nil {
		return err
	}

	// If the advanced scheduling feature is enabled, add the Syncer Finalizer to the upstream object
	if c.advancedSchedulingEnabled {
		if err := c.ensureSyncerFinalizer(ctx, gvr, upstreamObj); err != nil {
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

	if c.advancedSchedulingEnabled {
		specDiffPatch := upstreamObj.GetAnnotations()[workloadv1alpha1.ClusterSpecDiffAnnotationPrefix+c.workloadClusterName]
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

		// TODO: wipe things like finalizers, owner-refs and any other life-cycle fields. The life-cycle
		//       should exclusively owned by the syncer. Let's not some Kubernetes magic interfere with it.

		// TODO(jmprusi): When using syncer virtual workspace we would check the DeletionTimestamp on the upstream object, instead of the DeletionTimestamp annotation,
		//                as the virtual workspace will set the the deletionTimestamp() on the location view by a transformation.
		intendedToBeRemovedFromLocation := upstreamObj.GetAnnotations()[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+c.workloadClusterName] != ""

		// TODO(jmprusi): When using syncer virtual workspace this condition would not be necessary anymore, since directly tested on the virtual workspace side.
		stillOwnedByExternalActorForLocation := upstreamObj.GetAnnotations()[workloadv1alpha1.ClusterFinalizerAnnotationPrefix+c.workloadClusterName] != ""

		if intendedToBeRemovedFromLocation && !stillOwnedByExternalActorForLocation {
			if err := c.toClient.Resource(gvr).Namespace(downstreamNamespace).Delete(ctx, downstreamObj.GetName(), metav1.DeleteOptions{}); err != nil {
				if errors.IsNotFound(err) {
					// That's not an error.
					// Just think about removing the finalizer from the KCP location-specific resource:
					if err := ensureUpstreamFinalizerRemoved(ctx, gvr, c.fromClient, upstreamObj.GetNamespace(), c.workloadClusterName, c.upstreamClusterName, upstreamObj.GetName()); err != nil {
						return err
					}
					return nil
				}
				klog.Errorf("Error deleting %s %s/%s from downstream %s|%s/%s: %v", gvr.Resource, upstreamObj.GetNamespace(), upstreamObj.GetName(), upstreamObj.GetClusterName(), upstreamObj.GetNamespace(), upstreamObj.GetName(), err)
				return err
			}
			klog.V(2).Infof("Deleted %s %s/%s from downstream %s|%s/%s", gvr.Resource, upstreamObj.GetNamespace(), downstreamObj.GetName(), upstreamObj.GetClusterName(), upstreamObj.GetNamespace(), upstreamObj.GetName())
			return nil
		}
	}

	// Marshalling the unstructured object is good enough as SSA patch
	data, err := json.Marshal(downstreamObj)
	if err != nil {
		return err
	}

	if _, err := c.toClient.Resource(gvr).Namespace(downstreamNamespace).Patch(ctx, downstreamObj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: syncerApplyManager, Force: pointer.Bool(true)}); err != nil {
		klog.Errorf("Error upserting %s %s/%s from upstream %s|%s/%s: %v", gvr.Resource, downstreamObj.GetNamespace(), downstreamObj.GetName(), upstreamObj.GetClusterName(), upstreamObj.GetNamespace(), upstreamObj.GetName(), err)
		return err
	}
	klog.Infof("Upserted %s %s/%s from upstream %s|%s/%s", gvr.Resource, downstreamObj.GetNamespace(), downstreamObj.GetName(), upstreamObj.GetClusterName(), upstreamObj.GetNamespace(), upstreamObj.GetName())

	return nil
}
