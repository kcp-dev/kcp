/*
Copyright 2022 The KCP Authors.

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

package storage

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const (
	// DelayStatusSyncing instructs delaying the update of the PVC status back to KCP.
	DelayStatusSyncing = "internal.workload.kcp.dev/delaystatussyncing"
	upSyncDiff         = "internal.workload.kcp.dev/upsyncdiff"
)

func (c *PersistentVolumeClaimController) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	logger = logger.WithValues(logging.NamespaceKey, namespace, logging.NameKey, name)

	// Get the downstream PersistentVolumeClaim object for inspection
	downstreamPVCObject, err := c.getDownstreamPersistentVolumeClaim(name, namespace)
	if err != nil {
		// If the downstream PVC is not found, then we can assume that it has been deleted and we do not need to process it
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get downstream PersistentVolumeClaim: %w", err)
	}

	downstreamPVCUnstructured, ok := downstreamPVCObject.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("failed to assert object to Unstructured: %T", downstreamPVCObject)
	}

	downstreamPVC := &corev1.PersistentVolumeClaim{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(downstreamPVCUnstructured.UnstructuredContent(), downstreamPVC)
	if err != nil {
		return fmt.Errorf("failed to convert unstructured to PersistentVolumeClaim: %w", err)
	}

	// Copy the current downstream PVC object for comparison later
	old := downstreamPVC
	downstreamPVC = downstreamPVC.DeepCopy()

	logger.V(1).Info("processing downstream PersistentVolumeClaim")

	switch downstreamPVC.Status.Phase {
	case corev1.ClaimBound:
		nsObj, err := c.getDownstreamNamespace(downstreamPVC.Namespace)
		if err != nil {
			return err
		}

		if nsObj == nil {
			logger.V(4).Info("namespace is nil, ignoring key")
			return nil
		}

		ns, ok := nsObj.(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("failed to assert object to a Namespace: %T", nsObj)
		}

		// Fetch the namespace locator annotation from the downstream PVC, this will be helpful to
		// get the upstream PVC
		nsLocatorJSON, ok := ns.GetAnnotations()[shared.NamespaceLocatorAnnotation]
		if !ok || nsLocatorJSON == "" {
			return fmt.Errorf("downstream PersistentVolumeClaim %q does not have the %q annotation", downstreamPVC.Name, shared.NamespaceLocatorAnnotation)
		}

		// Convert the string from the annotation into a NamespaceLocator object
		nsLocator := shared.NamespaceLocator{}
		if err := json.Unmarshal([]byte(nsLocatorJSON), &nsLocator); err != nil {
			return fmt.Errorf("failed to unmarshal namespace locator from downstream PersistentVolumeClaim %q: %w", downstreamPVC.Name, err)
		}
		nsLocatorClusterName := nsLocator.ClusterName
		nsLocatorClusterNamespace := nsLocator.Namespace
		// Remove the "namespace" key from the namespace locator since PV are not namespaced
		nsLocator.Namespace = ""

		// Get the PV corresponding to the PVC
		downstreamPersistentVolumeName := downstreamPVC.Spec.VolumeName
		if downstreamPersistentVolumeName == "" {
			return fmt.Errorf("downstream PersistentVolumeClaim %q does not have a volume name", downstreamPVC.Name)
		}

		downstreamPVObject, err := c.getDownstreamPersistentVolume(downstreamPersistentVolumeName)
		if err != nil {
			return fmt.Errorf("failed to get downstream PersistentVolume: %w", err)
		}

		downstreamPVUnstructured, ok := downstreamPVObject.(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("failed to assert object to a PersistentVolume: %T", downstreamPVCObject)
		}

		downstreamPV := &corev1.PersistentVolume{}
		err = runtime.DefaultUnstructuredConverter.FromUnstructured(downstreamPVUnstructured.UnstructuredContent(), downstreamPV)
		if err != nil {
			return fmt.Errorf("failed to convert unstructured to PersistentVolumeClaim: %w", err)
		}

		nsLocatorJSONToByte, err := json.Marshal(nsLocator)
		if err != nil {
			return fmt.Errorf("failed to marshal blob %q: %w", nsLocatorJSON, err)
		}

		nsLocatorJSON = string(nsLocatorJSONToByte)

		// Update the PV with the namespace locator annotation
		annotations := downstreamPV.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[shared.NamespaceLocatorAnnotation] = nsLocatorJSON
		downstreamPV.SetAnnotations(annotations)

		// Get upstream PVC from downstream PVC
		upstreamPVCObject, err := c.getUpstreamPersistentVolumeClaim(nsLocatorClusterName, downstreamPVC.Name, nsLocatorClusterNamespace)
		if err != nil {
			return fmt.Errorf("failed to get upstream PersistentVolumeClaim: %w", err)
		}

		upstreamPVC, ok := upstreamPVCObject.(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("failed to assert object to a PersistentVolumeClaim: %T", upstreamPVCObject)
		}

		// Create a Json patch for the PV resource whose content would update the PVC reference to match the upstream PVC reference (namespace, uid, resource version, etc ...)
		upstreamClaimRef := &corev1.ObjectReference{
			Kind:            upstreamPVC.GetKind(),
			Namespace:       upstreamPVC.GetNamespace(),
			Name:            upstreamPVC.GetName(),
			UID:             upstreamPVC.GetUID(),
			APIVersion:      upstreamPVC.GetAPIVersion(),
			ResourceVersion: upstreamPVC.GetResourceVersion(),
		}

		upstreamClaimRefJson, err := json.Marshal(upstreamClaimRef)
		if err != nil {
			return fmt.Errorf("failed to marshal claim ref for upstream persistent volume: %w", err)
		}

		claimRefJSONPatchAnnotation := fmt.Sprintf(`[{"op":"replace","path":"/spec/claimRef","value": %s}]`, string(upstreamClaimRefJson))
		annotations = downstreamPV.GetAnnotations()
		annotations[upSyncDiff] = claimRefJSONPatchAnnotation
		downstreamPV.SetAnnotations(annotations)

		// Update the PV with the Upsync requestState label for the current SyncTarget to trigger
		// the Upsyncing of the PV to the upstream workspace
		labels := downstreamPV.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[workloadv1alpha1.ClusterResourceStateLabelPrefix+c.syncTarget.key] = string(workloadv1alpha1.ResourceStateUpsync)
		downstreamPV.SetLabels(labels)

		logger.V(1).Info("updating downstream PersistentVolume with upstream PersistentVolumeClaim claim ref", "PersistentVolume", downstreamPV.Name)
		_, err = c.updateDownstreamPersistentVolume(ctx, downstreamPV)
		if err != nil {
			return fmt.Errorf("failed to update PersistentVolume with %q annotation: %w", upSyncDiff, err)
		}

		logger.V(1).Info("successfully updated PersistentVolume claim ref with upstream PersistentVolumeClaim", "PersistentVolume", downstreamPV.Name, "PersistentVolumeClaim", upstreamPVC.GetName())

	case corev1.ClaimPending, "":
		_, ok = downstreamPVC.GetAnnotations()[DelayStatusSyncing]
		if !ok {
			logger.V(1).Info("annotating downstream PersistentVolumeClaim with delay status syncing")

			annotations := downstreamPVC.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations[DelayStatusSyncing] = "true"
			downstreamPVC.SetAnnotations(annotations)

			oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
			newResource := &Resource{ObjectMeta: downstreamPVC.ObjectMeta, Spec: &downstreamPVC.Spec, Status: &downstreamPVC.Status}

			// If the object being reconciled changed as a result, update it.
			err := c.commit(ctx, oldResource, newResource, downstreamPVC.Namespace)
			if err != nil {
				return fmt.Errorf("failed to commit PVC: %w", err)
			}

			logger.V(1).Info("downstream PersistentVolumeClaim annotated to delay status syncing")
		} else {
			logger.V(1).Info("downstream PersistentVolumeClaim already annotated to delay status syncing")
		}
		return nil

	case corev1.ClaimLost:
		logger.V(6).Info("Lost", "PVC", downstreamPVC.Name)
		// TODO(davidfestal): do we want to do something special for this case?
		// Maybe delete the PV object downstream, so that it will be removed upstream?
		return nil

	default:
		return fmt.Errorf("unknown PVC phase %q", downstreamPVC.Status.Phase)
	}

	logger.V(1).Info("done processing downstream PersistentVolumeClaim")
	return nil
}
