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
	"fmt"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

const (
	// NamespaceScheduled represents status of the scheduling process for this namespace.
	NamespaceScheduled conditionsv1alpha1.ConditionType = "NamespaceScheduled"
	// NamespaceReasonUnschedulable reason in NamespaceScheduled Namespace Condition
	// means that the scheduler can't schedule the namespace right now, e.g. due to a
	// lack of ready clusters being available.
	NamespaceReasonUnschedulable = "Unschedulable"
	// NamespaceReasonSchedulingDisabled reason in NamespaceScheduled Namespace Condition
	// means that the automated scheduling for this namespace is disabled, e.g., when it's
	// labelled with ScheduleDisabledLabel.
	NamespaceReasonSchedulingDisabled = "SchedulingDisabled"
	// NamespaceReasonPlacementInvalid reason in NamespaceScheduled Namespace Condition
	// means the placement annotation has invalid value.
	NamespaceReasonPlacementInvalid = "PlacementInvalid"
)

// statusReconciler updates conditions on the namespace.
type statusConditionReconciler struct {
	patchNamespace func(ctx context.Context, clusterName logicalcluster.Path, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error)
}

// ensureScheduledStatus ensures the status of the given namespace reflects the
// namespace's scheduled state.
func (r *statusConditionReconciler) reconcile(ctx context.Context, ns *corev1.Namespace) (reconcileStatus, *corev1.Namespace, error) {
	logger := klog.FromContext(ctx)
	updatedNs := setScheduledCondition(ns)

	if equality.Semantic.DeepEqual(ns.Status, updatedNs.Status) {
		return reconcileStatusContinue, ns, nil
	}

	patchBytes, err := statusPatchBytes(ns, updatedNs)
	if err != nil {
		return reconcileStatusStop, ns, err
	}
	logger.WithValues("patch", string(patchBytes)).V(2).Info("updating status for namespace")
	patchedNamespace, err := r.patchNamespace(ctx, logicalcluster.From(ns).Path(), ns.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	if err != nil {
		return reconcileStatusStop, ns, fmt.Errorf("failed to patch status on namespace %s|%s: %w", logicalcluster.From(ns), ns.Name, err)
	}

	return reconcileStatusContinue, patchedNamespace, nil
}

// statusPatchBytes returns the bytes required to patch status for the provided namespace from its old to new state.
func statusPatchBytes(old, new *corev1.Namespace) ([]byte, error) {
	oldData, err := json.Marshal(corev1.Namespace{
		Status: old.Status,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal existing status for namespace %s|%s: %w", logicalcluster.From(new), new.Name, err)
	}

	newData, err := json.Marshal(corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			UID:             new.UID,
			ResourceVersion: new.ResourceVersion,
		}, // to ensure they appear in the patch as preconditions
		Status: new.Status,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal new status for namespace %s|%s: %w", logicalcluster.From(new), new.Name, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return nil, fmt.Errorf("failed to create status patch for namespace %s|%s: %w", logicalcluster.From(new), new.Name, err)
	}
	return patchBytes, nil
}

// NamespaceConditionsAdapter enables the use of the conditions helper
// library with Namespaces.
type NamespaceConditionsAdapter struct {
	*corev1.Namespace
}

func (ca *NamespaceConditionsAdapter) GetConditions() conditionsv1alpha1.Conditions {
	conditions := conditionsv1alpha1.Conditions{}
	for _, c := range ca.Status.Conditions {
		conditions = append(conditions, conditionsv1alpha1.Condition{
			Type:   conditionsv1alpha1.ConditionType(c.Type),
			Status: c.Status,
			// Default to None because NamespaceCondition lacks a Severity field
			Severity:           conditionsv1alpha1.ConditionSeverityNone,
			LastTransitionTime: c.LastTransitionTime,
			Reason:             c.Reason,
			Message:            c.Message,
		})
	}
	return conditions
}

func (ca *NamespaceConditionsAdapter) SetConditions(conditions conditionsv1alpha1.Conditions) {
	nsConditions := []corev1.NamespaceCondition{}
	for _, c := range conditions {
		nsConditions = append(nsConditions, corev1.NamespaceCondition{
			Type:   corev1.NamespaceConditionType(c.Type),
			Status: c.Status,
			// Severity is ignored
			LastTransitionTime: c.LastTransitionTime,
			Reason:             c.Reason,
			Message:            c.Message,
		})
	}
	ca.Status.Conditions = nsConditions
}

func setScheduledCondition(ns *corev1.Namespace) *corev1.Namespace {
	updatedNs := ns.DeepCopy()
	conditionsAdapter := &NamespaceConditionsAdapter{updatedNs}

	_, found := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]
	if !found {
		conditions.MarkFalse(conditionsAdapter, NamespaceScheduled, NamespaceReasonUnschedulable,
			conditionsv1alpha1.ConditionSeverityNone, // NamespaceCondition doesn't support severity
			"No available placements")
		return updatedNs
	}

	synced, _ := syncedRemovingCluster(ns)
	if len(synced) == 0 {
		conditions.MarkFalse(conditionsAdapter, NamespaceScheduled, NamespaceReasonUnschedulable,
			conditionsv1alpha1.ConditionSeverityNone, // NamespaceCondition doesn't support severity
			"No available sync targets")
		return updatedNs
	}

	conditions.MarkTrue(conditionsAdapter, NamespaceScheduled)
	return updatedNs
}
