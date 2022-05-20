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
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	conditionsapi "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const (
	SchedulingDisabledLabel = "experimental.workloads.kcp.dev/scheduling-disabled"

	// WorkspaceSchedulableLabel on a workspace enables scheduling for the contents
	// of the workspace. It is applied by default to workspaces of type `Universal`.
	WorkspaceSchedulableLabel = "workloads.kcp.dev/schedulable"
)

var (
	scheduleRequirement           labels.Requirement
	scheduleEmptyLabelRequirement labels.Requirement
	unscheduledRequirement        labels.Requirement

	workspaceSchedulableRequirement labels.Requirement
)

func init() {
	// This matches namespaces that haven't been scheduled yet
	if req, err := labels.NewRequirement(DeprecatedScheduledClusterNamespaceLabel, selection.DoesNotExist, []string{}); err != nil {
		klog.Fatalf("error creating the cluster label requirement: %v", err)
	} else {
		unscheduledRequirement = *req
	}
	// This matches namespaces with the cluster label set to ""
	if req, err := labels.NewRequirement(DeprecatedScheduledClusterNamespaceLabel, selection.Equals, []string{""}); err != nil {
		klog.Fatalf("error creating the cluster label requirement: %v", err)
	} else {
		scheduleEmptyLabelRequirement = *req
	}
	// This matches namespaces that should be scheduled automatically by the namespace controller
	if req, err := labels.NewRequirement(SchedulingDisabledLabel, selection.DoesNotExist, []string{}); err != nil {
		klog.Fatalf("error creating the schedule label requirement: %v", err)
	} else {
		scheduleRequirement = *req
	}
	// This matches workspaces whose contents should be scheduled.
	if req, err := labels.NewRequirement(WorkspaceSchedulableLabel, selection.Equals, []string{"true"}); err != nil {
		klog.Fatalf("error creating the schedule label requirement: %v", err)
	} else {
		workspaceSchedulableRequirement = *req
	}
}

// reconcileNamespace is responsible for assigning a namespace to a cluster, if
// it does not already have one.
//
// After assigning (or if it's already assigned), this also updates all
// resources in the namespace to be assigned to the namespace's cluster.
func (c *Controller) reconcileNamespace(ctx context.Context, lclusterName logicalcluster.Name, ns *corev1.Namespace) error {
	klog.Infof("Reconciling namespace %s|%s", lclusterName, ns.Name)

	workspaceSchedulingEnabled, err := isWorkspaceSchedulable(c.workspaceLister.Get, logicalcluster.From(ns))
	if err != nil {
		return err
	}
	if !workspaceSchedulingEnabled {
		klog.V(4).Infof("Scheduling is disabled for the workspace of namespace %s|%s", lclusterName, ns.Name)
		return nil
	}

	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}

	ns, _, err = c.ensureNamespaceScheduledDeprecated(ctx, ns)
	if err != nil {
		return err
	}

	_, err = c.ensureScheduledStatus(ctx, ns)
	if err != nil {
		return err
	}

	return nil
}

// ensureScheduledStatus ensures the status of the given namespace reflects the
// namespace's scheduled state.
func (c *Controller) ensureScheduledStatus(ctx context.Context, ns *corev1.Namespace) (*corev1.Namespace, error) {
	updatedNs := setScheduledCondition(ns)

	if equality.Semantic.DeepEqual(ns.Status, updatedNs.Status) {
		return ns, nil
	}

	patchBytes, err := statusPatchBytes(ns, updatedNs)
	if err != nil {
		return ns, err
	}

	patchedNamespace, err := c.kubeClient.Cluster(logicalcluster.From(ns)).CoreV1().Namespaces().
		Patch(ctx, ns.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	if err != nil {
		return ns, fmt.Errorf("failed to patch status on namespace %s|%s: %w", logicalcluster.From(ns), ns.Name, err)
	}

	return patchedNamespace, nil
}

// schedulingClusterLabelPatchBytes returns a patch expressing an operation
// to add, replace to the given value, or delete the cluster assignment label on a
// namespace.
func schedulingClusterLabelPatchBytes(oldClusterName, newClusterName string) (types.PatchType, []byte, error) {
	patches := make(map[string]interface{})

	if newClusterName == "" && oldClusterName != "" {
		patches[DeprecatedScheduledClusterNamespaceLabel] = nil
		patches[workloadv1alpha1.InternalClusterResourceStateLabelPrefix+oldClusterName] = nil
	} else if newClusterName != "" && oldClusterName == "" {
		patches[DeprecatedScheduledClusterNamespaceLabel] = newClusterName
		patches[workloadv1alpha1.InternalClusterResourceStateLabelPrefix+newClusterName] = string(workloadv1alpha1.ResourceStateSync)
	} else {
		patches[DeprecatedScheduledClusterNamespaceLabel] = newClusterName
		patches[workloadv1alpha1.InternalClusterResourceStateLabelPrefix+oldClusterName] = nil
		patches[workloadv1alpha1.InternalClusterResourceStateLabelPrefix+newClusterName] = string(workloadv1alpha1.ResourceStateSync)
	}

	bs, err := json.Marshal(map[string]interface{}{"metadata": map[string]interface{}{"labels": patches}})
	if err != nil {
		return "", nil, err
	}
	return types.MergePatchType, bs, nil
}

// observeCluster is responsible for watching to see if the Cluster is happy;
// if it's not, any namespace assigned to that cluster with automatic scheduling
// will be unassigned.
//
// After the namespace is unassigned, it will be picked up by
// reconcileNamespace above and assigned to another happy cluster if one can be
// found.
func (c *Controller) observeCluster(ctx context.Context, cluster *workloadv1alpha1.WorkloadCluster) error {
	klog.V(2).Infof("Observing WorkloadCluster %s|%s", logicalcluster.From(cluster), cluster.Name)

	strategy, pendingCordon := enqueueStrategyForCluster(cluster)

	if pendingCordon {
		dur := time.Until(cluster.Spec.EvictAfter.Time)
		c.enqueueClusterAfter(cluster, dur)
	}

	clusterName := logicalcluster.From(cluster)

	switch strategy {
	case enqueueUnscheduled:
		var errs []error
		errs = append(errs, c.enqueueNamespaces(clusterName, labels.NewSelector().Add(unscheduledRequirement).Add(scheduleRequirement)))
		errs = append(errs, c.enqueueNamespaces(clusterName, labels.NewSelector().Add(scheduleEmptyLabelRequirement).Add(scheduleRequirement)))
		return errors.NewAggregate(errs)

	case enqueueScheduled:
		scheduledToCluster, err := labels.NewRequirement(DeprecatedScheduledClusterNamespaceLabel, selection.Equals, []string{cluster.Name})
		if err != nil {
			return err
		}
		return c.enqueueNamespaces(clusterName, labels.NewSelector().Add(*scheduledToCluster))

	case enqueueNothing:
		break

	default:
		return fmt.Errorf("unexpected enqueue strategy: %d", strategy)
	}

	return nil
}

// enqueueNamespaces adds all namespaces matching selector to the queue to allow for scheduling.
func (c *Controller) enqueueNamespaces(clusterName logicalcluster.Name, selector labels.Selector) error {
	// TODO(ncdc): use cluster scoped generated lister when available
	namespaces, err := c.namespaceLister.List(selector)
	if err != nil {
		return err
	}

	for _, namespace := range namespaces {
		if logicalcluster.From(namespace) != clusterName {
			continue
		}

		if namespaceBlocklist.Has(namespace.Name) {
			klog.V(2).Infof("Skipping syncing namespace %q", namespace.Name)
			continue
		}

		c.enqueueNamespace(namespace)
	}

	return nil
}

type clusterEnqueueStrategy int

const (
	enqueueScheduled clusterEnqueueStrategy = iota
	enqueueUnscheduled
	enqueueNothing
)

// enqueueStrategyForCluster determines what namespace enqueueing strategy
// should be used in response to a given cluster state. Also returns a boolean
// indication of whether to enqueue the cluster in the future to respond to a
// impending cordon event.
func enqueueStrategyForCluster(cl *workloadv1alpha1.WorkloadCluster) (strategy clusterEnqueueStrategy, pendingCordon bool) {
	ready := conditions.IsTrue(cl, conditionsapi.ReadyCondition)
	cordoned := cl.Spec.EvictAfter != nil && cl.Spec.EvictAfter.Time.Before(time.Now())
	if !ready || cordoned {
		// An unready or cordoned cluster requires revisiting the scheduling
		// for the namespaces currently scheduled to the cluster to ensure
		// rescheduling is performed.
		return enqueueScheduled, false
	}

	// For ready clusters, a future cordon event requires enqueueing the
	// cluster for processing at the time of the event.
	pendingCordon = cl.Spec.EvictAfter != nil && cl.Spec.EvictAfter.After(time.Now())

	if cl.Spec.Unschedulable {
		// A ready cluster marked unschedulable doesn't allow new
		// assignments and doesn't need rescheduling of existing
		// assignments.
		return enqueueNothing, pendingCordon
	}

	// The cluster is schedulable and not cordoned. Enqueue unscheduled
	// namespaces to allow them to schedule to the cluster.
	return enqueueUnscheduled, pendingCordon
}

// statusPatchBytes returns the bytes required to patch status for the
// provided namespace from its old to new state.
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

type getWorkspaceFunc func(name string) (*tenancyv1alpha1.ClusterWorkspace, error)

// isWorkspaceSchedulable indicates whether the contents of the workspace
// identified by the logical cluster name are schedulable.
func isWorkspaceSchedulable(getWorkspace getWorkspaceFunc, logicalClusterName logicalcluster.Name) (bool, error) {
	org, hasParent := logicalClusterName.Parent()
	if !hasParent {
		return false, nil
	}

	workspaceKey := clusters.ToClusterAwareKey(org, logicalClusterName.Base())
	workspace, err := getWorkspace(workspaceKey)
	if err != nil {
		return false, fmt.Errorf("failed to retrieve workspace with key %s", workspaceKey)
	}

	return workspaceSchedulableRequirement.Matches(labels.Set(workspace.Labels)), nil
}
