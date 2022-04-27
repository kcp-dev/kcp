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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	conditionsapi "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const (
	// NamespaceScheduled represents status of the scheduling process for this namespace.
	NamespaceScheduled conditionsapi.ConditionType = "NamespaceScheduled"
	// NamespaceReasonUnschedulable reason in NamespaceScheduled Namespace Condition
	// means that the scheduler can't schedule the namespace right now, e.g. due to a
	// lack of ready clusters being available.
	NamespaceReasonUnschedulable = "Unschedulable"
	// NamespaceReasonSchedulingDisabled reason in NamespaceScheduled Namespace Condition
	// means that the automated scheduling for this namespace is disabled, e.g., when it's
	// labelled with ScheduleDisabledLabel.
	NamespaceReasonSchedulingDisabled = "SchedulingDisabled"
)

// NamespaceConditionsAdapter enables the use of the conditions helper
// library with Namespaces.
type NamespaceConditionsAdapter struct {
	*corev1.Namespace
}

func (ca *NamespaceConditionsAdapter) GetConditions() conditionsapi.Conditions {
	conditions := conditionsapi.Conditions{}
	for _, c := range ca.Status.Conditions {
		conditions = append(conditions, conditionsapi.Condition{
			Type:   conditionsapi.ConditionType(c.Type),
			Status: c.Status,
			// Default to None because NamespaceCondition lacks a Severity field
			Severity:           conditionsapi.ConditionSeverityNone,
			LastTransitionTime: c.LastTransitionTime,
			Reason:             c.Reason,
			Message:            c.Message,
		})
	}
	return conditions
}

func (ca *NamespaceConditionsAdapter) SetConditions(conditions conditionsapi.Conditions) {
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

// IsScheduled returns whether the given namespace's status indicates
// it is scheduled.
func IsScheduled(ns *corev1.Namespace) bool {
	if condition := conditions.Get(&NamespaceConditionsAdapter{ns}, NamespaceScheduled); condition != nil {
		return condition.Status == corev1.ConditionTrue
	}
	return false
}

func setScheduledCondition(ns *corev1.Namespace) *corev1.Namespace {
	updatedNs := ns.DeepCopy()
	conditionsAdapter := &NamespaceConditionsAdapter{updatedNs}

	if !scheduleRequirement.Matches(labels.Set(ns.Labels)) {
		// Scheduling disabled
		conditions.MarkFalse(conditionsAdapter, NamespaceScheduled, NamespaceReasonSchedulingDisabled,
			conditionsv1alpha1.ConditionSeverityNone, // NamespaceCondition doesn't support severity
			"Automatic scheduling is deactivated and can be performed by setting the cluster label manually.")
	} else if !hasWorkloadClusterAssigned(ns) {
		// Unschedulable
		conditions.MarkFalse(conditionsAdapter, NamespaceScheduled, NamespaceReasonUnschedulable,
			conditionsv1alpha1.ConditionSeverityNone, // NamespaceCondition doesn't support severity
			"No clusters are available to schedule Namespaces to.")
	} else {
		conditions.MarkTrue(conditionsAdapter, NamespaceScheduled)
	}

	return updatedNs
}

// hasWorkloadClusterAssigned returns whether the given namespace has a workload cluster assigned.
func hasWorkloadClusterAssigned(ns *corev1.Namespace) bool {
	hasWorkloadAssigned := false
	for clusterName, val := range workloadClustersFromLabels(ns.Labels) {
		if val != "" && ns.Annotations[LocationDeletionAnnotationName(clusterName)] == "" {
			hasWorkloadAssigned = true
		}
	}
	return hasWorkloadAssigned
}
