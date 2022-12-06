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

package placement

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/logicalcluster/v3"
)

// placementNamespaceReconciler checks the namespaces bound to this placement and set the phase.
// If there are at least one namespace bound to this placement, the placement is in bound state.
type placementNamespaceReconciler struct {
	listNamespacesWithAnnotation func(clusterName logicalcluster.Name) ([]*corev1.Namespace, error)
}

func (r *placementNamespaceReconciler) reconcile(ctx context.Context, placement *schedulingv1alpha1.Placement) (reconcileStatus, *schedulingv1alpha1.Placement, error) {
	if placement.Status.Phase == schedulingv1alpha1.PlacementPending {
		return reconcileStatusContinue, placement, nil
	}

	if placement.Status.SelectedLocation == nil {
		placement.Status.Phase = schedulingv1alpha1.PlacementPending
		return reconcileStatusContinue, placement, nil
	}

	// found all namespaces using placement's namespace selector who also have the placement annotation key.
	nss, err := r.selectNamespaces(placement)
	if err != nil {
		return reconcileStatusContinue, placement, err
	}

	if len(nss) > 0 {
		placement.Status.Phase = schedulingv1alpha1.PlacementBound
	} else {
		placement.Status.Phase = schedulingv1alpha1.PlacementUnbound
	}

	return reconcileStatusContinue, placement, err
}

func (r *placementNamespaceReconciler) selectNamespaces(placement *schedulingv1alpha1.Placement) ([]*corev1.Namespace, error) {
	clusterName := logicalcluster.From(placement)
	nss, err := r.listNamespacesWithAnnotation(clusterName)

	if err != nil {
		return nil, err
	}

	selector, err := metav1.LabelSelectorAsSelector(placement.Spec.NamespaceSelector)
	if err != nil {
		return nil, err
	}

	candidates := []*corev1.Namespace{}
	var errs []error
	for _, ns := range nss {
		if !selector.Matches(labels.Set(ns.Labels)) {
			continue
		}

		candidates = append(candidates, ns)
	}

	return candidates, utilserrors.NewAggregate(errs)
}
