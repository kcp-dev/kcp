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

package namespace

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/kcp-dev/logicalcluster"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	reconcilerplacement "github.com/kcp-dev/kcp/pkg/reconciler/scheduling/placement"
)

// reconcileNamespace is responsible for assigning a namespace to a cluster, if
// it does not already have one.
//
// After assigning (or if it's already assigned), this also updates all
// resources in the namespace to be assigned to the namespace's cluster.
func (c *Controller) reconcileNamespace(ctx context.Context, lclusterName logicalcluster.Name, ns *corev1.Namespace) error {
	klog.Infof("Reconciling namespace %s|%s", lclusterName, ns.Name)

	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}

	var placementAnnotation string
	placementAnnotation, isPlaced := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]
	if isPlaced {
		var placement schedulingv1alpha1.PlacementAnnotation
		if err := json.Unmarshal([]byte(placementAnnotation), &placement); err != nil {
			klog.Errorf("error unmarshalling %s annotation on namespace %s|%s: %v", schedulingv1alpha1.PlacementAnnotationKey, logicalcluster.From(ns), ns.Name, err)
			placement = schedulingv1alpha1.PlacementAnnotation{} // an invalid placement annotation is treated as if it wasn't set
			// fall through
		}

		if _, _, err := c.ensureNamespaceScheduled(ctx, ns, placement); err != nil {
			return err
		}
	}

	return nil
}

// ensureNamespaceScheduled will ensure every placement to a location is properly represented in the
// state.workloads.kcp.dev/* labels.
func (c *Controller) ensureNamespaceScheduled(ctx context.Context, ns *corev1.Namespace, placement schedulingv1alpha1.PlacementAnnotation) (*corev1.Namespace, bool, error) {
	expectedPlacement := map[string]interface{}{}   // nil means to remove the key
	expectedAnnotations := map[string]interface{}{} // nil means to remove the key
	expectedLabels := map[string]interface{}{}      // nil means to remove the key
	for key, state := range placement {
		_, _, workloadCluster, valid := reconcilerplacement.ParsePlacementString(key)
		if !valid {
			return nil, false, fmt.Errorf("invalid key %q", key)
		}
		expectedPlacement, expectedAnnotations, expectedLabels = applyPlacement(key, state, workloadCluster, ns, expectedPlacement, expectedAnnotations, expectedLabels)
	}

	// compute patch
	if len(expectedPlacement) > 0 {
		newPlacement := make(schedulingv1alpha1.PlacementAnnotation, len(expectedPlacement))
		for k, v := range newPlacement {
			if placement, found := expectedPlacement[k]; found && placement == nil {
				continue
			} else if found && placement != nil {
				newPlacement[k] = schedulingv1alpha1.PlacementState(expectedPlacement[k].(string))
			} else {
				newPlacement[k] = v
			}
		}
		if !reflect.DeepEqual(newPlacement, placement) {
			bs, err := json.Marshal(newPlacement)
			if err != nil {
				return nil, false, err
			}
			expectedAnnotations[schedulingv1alpha1.PlacementAnnotationKey] = string(bs)
		}
	}
	if len(expectedAnnotations) == 0 && len(expectedLabels) == 0 {
		return ns, false, nil
	}
	patch := map[string]interface{}{}
	if len(expectedAnnotations) > 0 {
		if err := unstructured.SetNestedField(patch, expectedAnnotations, "metadata", "annotations"); err != nil {
			klog.Errorf("unexpected unstructured error: %v", err)
			return ns, false, nil
		}
	}
	if len(expectedLabels) > 0 {
		if err := unstructured.SetNestedField(patch, expectedLabels, "metadata", "labels"); err != nil {
			klog.Errorf("unexpected unstructured error: %v", err)
			return ns, false, nil
		}
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create patch for namespace %s|%s: %w", logicalcluster.From(ns), ns.Name, err)
	}
	klog.V(3).Infof("Patching to update workload cluster information on namespace %s|%s: %s",
		logicalcluster.From(ns), ns.Name, string(patchBytes))
	patchedNamespace, err := c.kubeClient.Cluster(logicalcluster.From(ns)).CoreV1().Namespaces().
		Patch(ctx, ns.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return ns, false, err
	}

	return patchedNamespace, true, nil
}

// applyPlacement computes the new scheduling annotations and labels based on placement, and potentially sets itself Unbound in the placement annotation. Nil means to delete the key.
func applyPlacement(placementKey string, state schedulingv1alpha1.PlacementState, workloadCluster string, ns *corev1.Namespace, expectedPlacements, expectedAnnotations, expectedLabels map[string]interface{}) (newPlacement, newAnnotation, newLabels map[string]interface{}) {
	stateLabelKey := workloadv1alpha1.InternalClusterResourceStateLabelPrefix + workloadCluster
	deletionAnnotationKey := workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix + workloadCluster
	existingLabels := ns.Labels

	switch state {
	case schedulingv1alpha1.PlacementStatePending:
		// ensure the state.workloads.kcp.dev/* label is set
		// TODO(sttts): add UID lookup for the workload cluster here
		if state, found := existingLabels[stateLabelKey]; !found || state != string(workloadv1alpha1.ResourceStateSync) {
			klog.V(3).Infof("Setting %s label on namespace %s|%s to %s", stateLabelKey, logicalcluster.From(ns), ns.Name, workloadCluster)
		}
		expectedLabels[stateLabelKey] = string(workloadv1alpha1.ResourceStateSync)
	case schedulingv1alpha1.PlacementStateBound:
		if state, found := existingLabels[stateLabelKey]; !found {
			klog.Warningf("Found no %s label on namespace %s|%s, but found a bound placement for %s", stateLabelKey, logicalcluster.From(ns), ns.Name, workloadCluster)

		} else if state != string(workloadv1alpha1.ResourceStateSync) {
			klog.Warningf("Found unexpected %s=%q label on namespace %s|%s, but found a bound placement for %s", stateLabelKey, state, logicalcluster.From(ns), ns.Name, workloadCluster)
		}
		klog.V(3).Infof("Setting %s=%q label on namespace %s|%s to %s", stateLabelKey, string(workloadv1alpha1.ResourceStateSync), logicalcluster.From(ns), ns.Name, workloadCluster)
		expectedLabels[stateLabelKey] = string(workloadv1alpha1.ResourceStateSync)
	case schedulingv1alpha1.PlacementStateRemoving:
		if _, found := existingLabels[stateLabelKey]; !found {
			klog.V(3).Infof("Label %s is gone from namespace %s|%s being removed from %s. Setting to Unbound and deleting %s", stateLabelKey, logicalcluster.From(ns), ns.Name, workloadCluster, deletionAnnotationKey)
			expectedPlacements[placementKey] = string(schedulingv1alpha1.PlacementStateUnbound)
			expectedAnnotations[deletionAnnotationKey] = nil
		} else if ts, found := existingLabels[deletionAnnotationKey]; !found || ts == "" {
			now := time.Now().UTC().Format(time.RFC3339)
			klog.V(3).Infof("Setting %s=%q label on namespace %s|%s because placement is Removing for %s", deletionAnnotationKey, now, logicalcluster.From(ns), ns.Name, workloadCluster)
			expectedAnnotations[deletionAnnotationKey] = now
		}
	case schedulingv1alpha1.PlacementStateUnbound:
		if state, found := existingLabels[stateLabelKey]; found {
			klog.Warningf("Found unexpected %s=%q label on namespace %s|%s, because Unbound placement for %s. Removing the label", stateLabelKey, state, logicalcluster.From(ns), ns.Name, workloadCluster)
			expectedLabels[stateLabelKey] = nil
		} else if ts, found := existingLabels[deletionAnnotationKey]; found {
			klog.V(3).Infof("Removing %s=%q label on namespace %s|%s because it is now Unbound for %s", stateLabelKey, ts, logicalcluster.From(ns), ns.Name, workloadCluster)
			expectedAnnotations[deletionAnnotationKey] = nil
		}
	default:
		// do nothing. Leave the label. We might be in a downgrade scenario.
	}
	return expectedPlacements, expectedAnnotations, expectedLabels
}
