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

	corev1 "k8s.io/api/core/v1"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/scheduling/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
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

// reconcileStatus ensures the status of the given namespace reflects the
// namespace's scheduled state.
func (c *controller) reconcileStatus(_ context.Context, _ string, ns *corev1.Namespace) (reconcileResult, error) {
	conditionsAdapter := &NamespaceConditionsAdapter{ns}

	_, found := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]
	if !found {
		conditions.MarkFalse(conditionsAdapter, NamespaceScheduled, NamespaceReasonUnschedulable,
			conditionsv1alpha1.ConditionSeverityNone, // NamespaceCondition doesn't support severity
			"No available placements")
		return reconcileResult{}, nil
	}

	syncStatus := syncStatusFor(ns)
	if len(syncStatus.active) == 0 {
		conditions.MarkFalse(conditionsAdapter, NamespaceScheduled, NamespaceReasonUnschedulable,
			conditionsv1alpha1.ConditionSeverityNone, // NamespaceCondition doesn't support severity
			"No available sync targets")
		return reconcileResult{}, nil
	}

	conditions.MarkTrue(conditionsAdapter, NamespaceScheduled)
	return reconcileResult{}, nil
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
