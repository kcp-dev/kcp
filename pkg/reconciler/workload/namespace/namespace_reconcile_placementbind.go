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

package namespace

import (
	"context"
	"encoding/json"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

// bindNamespaceReconciler updates the existing annotation and creates an empty one if
// at least one placement matches and there is no annotation. It delete the annotation
// if there is no matched placement.
// TODO this should be reconsidered when we want lazy binding.
type bindNamespaceReconciler struct {
	listPlacement func(clusterName logicalcluster.Name) ([]*schedulingv1alpha1.Placement, error)

	patchNamespace func(ctx context.Context, clusterName logicalcluster.Name, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error)
}

func (r *bindNamespaceReconciler) reconcile(ctx context.Context, ns *corev1.Namespace) (reconcileStatus, *corev1.Namespace, error) {
	logger := klog.FromContext(ctx)
	clusterName := logicalcluster.From(ns)

	_, foundPlacement := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]

	validPlacements, err := r.validPlacements(clusterName, ns)
	if err != nil {
		return reconcileStatusContinue, ns, err
	}

	expectedAnnotations := map[string]interface{}{} // nil means to remove the key
	if len(validPlacements) > 0 && !foundPlacement {
		expectedAnnotations[schedulingv1alpha1.PlacementAnnotationKey] = ""
	} else if len(validPlacements) == 0 && foundPlacement {
		expectedAnnotations[schedulingv1alpha1.PlacementAnnotationKey] = nil
	}

	if len(expectedAnnotations) == 0 {
		return reconcileStatusContinue, ns, nil
	}

	patch := map[string]interface{}{}
	if err := unstructured.SetNestedField(patch, expectedAnnotations, "metadata", "annotations"); err != nil {
		return reconcileStatusStop, ns, err
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return reconcileStatusStop, ns, err
	}
	logger.WithValues("patch", string(patchBytes)).V(3).Info("patching Namespace to update placement annotation")
	updated, err := r.patchNamespace(ctx, clusterName, ns.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return reconcileStatusStop, ns, err
	}

	return reconcileStatusContinue, updated, nil
}

func (r *bindNamespaceReconciler) validPlacements(clusterName logicalcluster.Name, ns *corev1.Namespace) ([]*schedulingv1alpha1.Placement, error) {
	placements, err := r.listPlacement(clusterName)

	if err != nil {
		return nil, err
	}

	return filterValidPlacements(ns, placements), nil
}

func filterValidPlacements(ns *corev1.Namespace, placements []*schedulingv1alpha1.Placement) []*schedulingv1alpha1.Placement {
	var candidates []*schedulingv1alpha1.Placement
	for _, placement := range placements {
		if placement.Status.Phase == schedulingv1alpha1.PlacementPending {
			continue
		}
		if conditions.IsFalse(placement, schedulingv1alpha1.PlacementReady) {
			continue
		}
		if isPlacementValidForNS(ns, placement) {
			candidates = append(candidates, placement)
		}
	}

	return candidates
}

func isPlacementValidForNS(ns *corev1.Namespace, placement *schedulingv1alpha1.Placement) bool {
	selector, err := metav1.LabelSelectorAsSelector(placement.Spec.NamespaceSelector)
	if err != nil {
		return false
	}

	return selector.Matches(labels.Set(ns.Labels))
}
