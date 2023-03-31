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

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

// reconcilePlacementBind updates the existing scheduling.kcp.io/placement annotation and creates an
// empty one if at least one placement matches and there is no annotation. It deletes the annotation
// if there is no matched placement.
//
// TODO this should be reconsidered when we want lazy binding.
func (c *controller) reconcilePlacementBind(
	_ context.Context,
	_ string,
	ns *corev1.Namespace,
) (reconcileResult, error) {
	clusterName := logicalcluster.From(ns)

	validPlacements, err := c.validPlacements(clusterName, ns)
	if err != nil {
		return reconcileResult{stop: true}, err
	}

	_, hasPlacement := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]
	shouldHavePlacement := len(validPlacements) > 0

	switch {
	case shouldHavePlacement && hasPlacement, !shouldHavePlacement && !hasPlacement:
		return reconcileResult{}, nil
	case shouldHavePlacement && !hasPlacement:
		if ns.Annotations == nil {
			ns.Annotations = make(map[string]string)
		}
		ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey] = ""
	case !shouldHavePlacement && hasPlacement:
		delete(ns.Annotations, schedulingv1alpha1.PlacementAnnotationKey)
	}

	return reconcileResult{stop: true}, nil
}

func (c *controller) validPlacements(clusterName logicalcluster.Name, ns *corev1.Namespace) ([]*schedulingv1alpha1.Placement, error) {
	placements, err := c.listPlacements(clusterName)

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
