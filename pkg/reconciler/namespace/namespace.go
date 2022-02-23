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
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const (
	ClusterLabel            = "workloads.kcp.dev/cluster"
	SchedulingDisabledLabel = "experimental.workloads.kcp.dev/scheduling-disabled"
)

var (
	scheduleRequirement           labels.Requirement
	scheduleEmptyLabelRequirement labels.Requirement
	unscheduledRequirement        labels.Requirement
)

func init() {
	// This matches namespaces that haven't been scheduled yet
	if req, err := labels.NewRequirement(ClusterLabel, selection.DoesNotExist, []string{}); err != nil {
		klog.Fatalf("error creating the cluster label requirement: %v", err)
	} else {
		unscheduledRequirement = *req
	}
	// This matches namespaces with the cluster label set to ""
	if req, err := labels.NewRequirement(ClusterLabel, selection.Equals, []string{""}); err != nil {
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
}

// reconcileResource is responsible for setting the cluster for a resource of
// any type, to match the cluster where its namespace is assigned.
func (c *Controller) reconcileResource(ctx context.Context, lclusterName string, unstr *unstructured.Unstructured, gvr *schema.GroupVersionResource) error {
	if gvr.Group == "networking.k8s.io" && gvr.Resource == "ingresses" {
		klog.V(2).Infof("Skipping reconciliation of ingress %s/%s", unstr.GetNamespace(), unstr.GetName())
		return nil
	}

	klog.Infof("Reconciling %s %s|%s/%s", gvr.String(), lclusterName, unstr.GetNamespace(), unstr.GetName())

	// If the resource is not namespaced (incl if the resource is itself a
	// namespace), ignore it.
	if unstr.GetNamespace() == "" {
		klog.V(5).Infof("%s %s|%s had no namespace; ignoring", gvr.String(), unstr.GetClusterName(), unstr.GetName())
		return nil
	}

	// Align the resource's assigned cluster with the namespace's assigned
	// cluster.
	// First, get the namespace object (from the cached lister).
	ns, err := c.namespaceLister.Get(clusters.ToClusterAwareKey(lclusterName, unstr.GetNamespace()))
	if err != nil {
		return err
	}

	if !scheduleRequirement.Matches(labels.Set(ns.Labels)) {
		// Do not schedule the resource transitively, and let external controllers
		// or users be responsible for it, consistently with the scheduling of the
		// namespace.
		return nil
	}

	lbls := unstr.GetLabels()
	if lbls == nil {
		lbls = map[string]string{}
	}

	old, new := lbls[ClusterLabel], ns.Labels[ClusterLabel]
	if old == new {
		// Already assigned to the right cluster.
		return nil
	}

	// Update the resource's assignment.
	patchType, patchBytes := assignedClusterPatchBytes(new)
	if _, err = c.dynClient.Cluster(lclusterName).Resource(*gvr).Namespace(ns.Name).
		Patch(ctx, unstr.GetName(), patchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return err
	}
	klog.Infof("Patched cluster assignment for %s %s/%s: %q -> %q", gvr, ns.Name, unstr.GetName(), old, new)

	return nil
}

func (c *Controller) reconcileGVR(ctx context.Context, gvr schema.GroupVersionResource) error {
	// Update all resources in the namespace with the cluster assignment.
	listers, _ := c.ddsif.Listers()
	lister, found := listers[gvr]
	if !found {
		return fmt.Errorf("informer for %s is not synced; re-enqueueing", gvr)
	}

	// Enqueue workqueue items to reconcile every resource of this type, in
	// all namespaces.
	objs, err := lister.List(labels.Everything())
	if err != nil {
		return err
	}
	for _, obj := range objs {
		c.enqueueResource(gvr, obj)
	}
	return nil
}

func (c *Controller) patchStatus(ctx context.Context, ns *corev1.Namespace, updateConditions updateConditionsFunc) error {
	patchBytes, err := statusPatchBytes(ns, updateConditions)
	if err != nil {
		return err
	}
	_, err = c.kubeClient.Cluster(ns.ClusterName).CoreV1().Namespaces().
		Patch(ctx, ns.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	if err != nil {
		return fmt.Errorf("failed to patch status on namespace %q|%q: %w", ns.ClusterName, ns.Name, err)
	}
	return nil
}

// Set the scheduled condition on namespaces and not the resources it
// contains. Not all native types support status conditions (e.g. configmaps
// and secrets) and there is no guarantee that arbitrary CRDs will support
// conditions.
func (c *Controller) updateScheduledCondition(ctx context.Context, ns *corev1.Namespace, scheduled, disabled bool) error {
	klog.Infof("Patching namespace %q|%q status to indicate scheduled=%t", ns.ClusterName, ns.Name, scheduled)

	if scheduled {
		return c.patchStatus(ctx, ns, func(conditionsSetter conditions.Setter) {
			conditions.MarkTrue(conditionsSetter, NamespaceScheduled)
		})
	}

	if disabled {
		return c.patchStatus(ctx, ns, func(conditionsSetter conditions.Setter) {
			conditions.MarkFalse(conditionsSetter, NamespaceScheduled, NamespaceReasonSchedulingDisabled,
				conditionsv1alpha1.ConditionSeverityNone, // NamespaceCondition doesn't support severity
				"Automatic scheduling is deactivated and can be performed by setting the cluster label manually.")
		})
	}

	return c.patchStatus(ctx, ns, func(conditionsSetter conditions.Setter) {
		conditions.MarkFalse(conditionsSetter, NamespaceScheduled, NamespaceReasonUnschedulable,
			conditionsv1alpha1.ConditionSeverityNone, // NamespaceCondition doesn't support severity
			"No clusters are available to schedule Namespaces to.")
	})
}

// ensureScheduled attempts to ensure the namespace is assigned to a viable cluster. This
// will succeed without error if a cluster is assigned or if there are no viable clusters
// to assign to. The condition of not being scheduled to a cluster will be reflected in
// the namespace's status rather than by returning an error.
func (c *Controller) ensureScheduled(ctx context.Context, ns *corev1.Namespace) error {
	oldPClusterName := ns.Labels[ClusterLabel]

	scheduler := namespaceScheduler{
		getCluster:   c.clusterLister.Get,
		listClusters: c.clusterLister.List,
	}
	newPClusterName, err := scheduler.AssignCluster(ns)
	if err != nil {
		return err
	}

	if oldPClusterName == newPClusterName {
		return nil
	}

	klog.Infof("Patching to update cluster assignment for namespace %q|%q: %q -> %q",
		ns.ClusterName, ns.Name, oldPClusterName, newPClusterName)
	patchType, patchBytes := clusterLabelPatchBytes(newPClusterName)
	_, err = c.kubeClient.Cluster(ns.ClusterName).CoreV1().Namespaces().
		Patch(ctx, ns.Name, patchType, patchBytes, metav1.PatchOptions{})
	return err
}

// ensureScheduledStatus ensures the status of the given namespace reflects whether the
// namespace has been assigned to a cluster.
func (c *Controller) ensureScheduledStatus(ctx context.Context, ns *corev1.Namespace) error {
	scheduled := ns.Labels[ClusterLabel] != ""
	disabled := !scheduleRequirement.Matches(labels.Set(ns.Labels))

	if condition := conditions.Get(&NamespaceConditionsAdapter{ns}, NamespaceScheduled); condition != nil {
		conditionStatusChanged := scheduled != (condition.Status == corev1.ConditionTrue)
		conditionReasonChanged := disabled && condition.Reason != NamespaceReasonSchedulingDisabled ||
			!disabled && condition.Reason != NamespaceReasonUnschedulable
		statusChangeRequired := conditionStatusChanged || conditionReasonChanged
		if statusChangeRequired {
			return c.updateScheduledCondition(ctx, ns, scheduled, disabled)
		}
		return nil
	}

	return c.updateScheduledCondition(ctx, ns, scheduled, disabled)
}

// reconcileNamespace is responsible for assigning a namespace to a cluster, if
// it does not already have one.
//
// After assigning (or if it's already assigned), this also updates all
// resources in the namespace to be assigned to the namespace's cluster.
func (c *Controller) reconcileNamespace(ctx context.Context, lclusterName string, ns *corev1.Namespace) error {
	klog.Infof("Reconciling namespace %q|%q", lclusterName, ns.Name)

	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}

	if err := c.ensureScheduled(ctx, ns); err != nil {
		return err
	}

	if err := c.ensureScheduledStatus(ctx, ns); err != nil {
		return err
	}

	// Update all resources in the namespace with the cluster assignment.
	//
	// TODO(sttts): don't requeue all gvr just because of a tiny update on a namespace
	//
	// including updates that don't affect cluster assignment (e.g.,
	// annotation). This will be especially painful at startup, since all
	// resources are already enqueued and reconciled separately. Add some
	// logic to filter out un-interesting updates, and consider only
	// enqueueing items here if they're not already enqueued.
	listers, notSynced := c.ddsif.Listers()
	for gvr, lister := range listers {
		klog.Infof("Enqueuing resources in namespace %q|%q for GVR %q", lclusterName, ns.Name, gvr)
		objs, err := lister.ByNamespace(ns.Name).List(labels.Everything())
		if err != nil {
			return err
		}
		for _, obj := range objs {
			c.enqueueResource(gvr, obj)
		}
	}
	// For all types whose informer hasn't synced yet, enqueue a workqueue
	// item to check that GVR again later (reconcileGVR, above).
	for _, gvr := range notSynced {
		klog.Infof("Informer for %s is not synced; re-enqueueing", gvr)
		c.enqueueGVR(gvr)
	}

	return nil
}

// assignedClusterPatchBytes returns JSON patch bytes expressing an operation
// to add, replace to the given value, or delete the cluster assignment label.
func assignedClusterPatchBytes(val string) (types.PatchType, []byte) {
	if val == "" {
		return types.JSONPatchType,
			[]byte(fmt.Sprintf(`[{"op": "remove", "path": "/metadata/labels/%s"}]`, strings.ReplaceAll(ClusterLabel, "/", "~1")))
	}

	return types.MergePatchType,
		[]byte(fmt.Sprintf(`
{
  "metadata":{
    "labels":{
      %q: %q
    }
  }
}`, ClusterLabel, val))
}

// observeCluster is responsible for watching to see if the Cluster is happy;
// if it's not, any namespace assigned to that cluster with automatic scheduling
// will be unassigned.
//
// After the namespace is unassigned, it will be picked up by
// reconcileNamespace above and assigned to another happy cluster if one can be
// found.
func (c *Controller) observeCluster(ctx context.Context, cl *clusterv1alpha1.Cluster) error {
	klog.Infof("Observing Cluster %s", cl.Name)

	strategy, pendingCordon := enqueueStrategyForCluster(cl)

	if pendingCordon {
		dur := time.Until(cl.Spec.EvictAfter.Time)
		c.enqueueClusterAfter(cl, dur)
	}

	switch strategy {
	case enqueueUnscheduled:
		var errs []error
		errs = append(errs, c.enqueueNamespaces(ctx, labels.NewSelector().
			Add(unscheduledRequirement).Add(scheduleRequirement)))
		errs = append(errs, c.enqueueNamespaces(ctx, labels.NewSelector().
			Add(scheduleEmptyLabelRequirement).Add(scheduleRequirement)))
		return errors.NewAggregate(errs)

	case enqueueScheduled:
		scheduledToCluster, err := labels.NewRequirement(ClusterLabel, selection.Equals, []string{cl.Name})
		if err != nil {
			return err
		}
		return c.enqueueNamespaces(ctx, labels.NewSelector().Add(*scheduledToCluster))

	case enqueueNothing:
		break

	default:
		return fmt.Errorf("unexpected enqueue strategy: %d", strategy)
	}

	return nil
}

// enqueueNamespaces adds all namespaces matching selector to the queue to allow for scheduling.
func (c *Controller) enqueueNamespaces(ctx context.Context, selector labels.Selector) error {
	namespaces, err := c.namespaceLister.ListWithContext(ctx, selector)
	if err != nil {
		return err
	}
	for _, namespace := range namespaces {
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
func enqueueStrategyForCluster(cl *clusterv1alpha1.Cluster) (strategy clusterEnqueueStrategy, pendingCordon bool) {
	ready := conditions.IsTrue(cl, clusterv1alpha1.ClusterReadyCondition)
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
