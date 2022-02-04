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
	"encoding/json"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	conditionsapi "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const (
	// NamespaceScheduled represents status of the scheduling process for this namespace.
	NamespaceScheduled conditionsapi.ConditionType = "NamespaceScheduled"
	// NamespaceReasonUnschedulable reason in NamespaceScheduled Namespace Condition
	// means that the scheduler can't schedule the namespace right now, e.g. due to a
	// lack of ready clusters being available.
	NamespaceReasonUnschedulable = "Unschedulable"
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

type updateConditionsFunc func(conditionsSetter conditions.Setter)

// statusPatchBytes returns the bytes required to patch status for the
// provided namespace from its original state to the state after it
// has been mutated by the provided function.
func statusPatchBytes(ns *corev1.Namespace, updateConditions updateConditionsFunc) ([]byte, error) {
	oldData, err := json.Marshal(corev1.Namespace{
		Status: ns.Status,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal existing status for namespace %q: %w", ns.Name, err)
	}

	// Mutation is assumed safe
	conditionsAdapter := &NamespaceConditionsAdapter{ns}
	updateConditions(conditionsAdapter)

	newData, err := json.Marshal(corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			UID:             ns.UID,
			ResourceVersion: ns.ResourceVersion,
		}, // to ensure they appear in the patch as preconditions
		Status: ns.Status,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal new status for namespace %q: %w", ns.Name, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return nil, fmt.Errorf("failed to create status patch for namespace %q: %w", ns.Name, err)
	}
	return patchBytes, nil
}

// HasScheduledStatus returns whether the given namespace's status
// indicates it is scheduled.
func HasScheduledStatus(ns *corev1.Namespace) bool {
	for _, condition := range ns.Status.Conditions {
		if condition.Type == corev1.NamespaceConditionType(NamespaceScheduled) {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
