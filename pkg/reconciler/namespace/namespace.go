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
	"math/rand"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const clusterLabel = "kcp.dev/cluster"

// reconcileResource is responsible for setting the cluster for a resource of
// any type, to match the cluster where its namespace is assigned.
func (c *Controller) reconcileResource(ctx context.Context, lclusterName string, unstr *unstructured.Unstructured, gvr *schema.GroupVersionResource) error {
	if gvr.Group == "networking.k8s.io" && gvr.Resource == "ingresses" {
		klog.V(2).Infof("Skipping reconciliation of ingress %s/%s", unstr.GetNamespace(), unstr.GetName())
		return nil
	}

	klog.Infof("Reconciling %s %s/%s", gvr.String(), unstr.GetNamespace(), unstr.GetName())

	// If the resource is not namespaced (incl if the resource is itself a
	// namespace), ignore it.
	if unstr.GetNamespace() == "" {
		klog.Infof("%s %s had no namespace; ignoring", gvr.String(), unstr.GetName())
		return nil
	}

	// Align the resource's assigned cluster with the namespace's assigned
	// cluster.
	// First, get the namespace object (from the cached lister).
	ns, err := c.namespaceLister.Get(clusters.ToClusterAwareKey(lclusterName, unstr.GetNamespace()))
	if err != nil {
		return err
	}

	lbls := unstr.GetLabels()
	if lbls == nil {
		lbls = map[string]string{}
	}

	old, new := lbls[clusterLabel], ns.Labels[clusterLabel]
	if old == new {
		// Already assigned to the right cluster.
		return nil
	}

	// Update the resource's assignment.
	klog.Infof("Patching to update cluster assignment for %s %s/%s: %q -> %q", gvr, ns.Name, unstr.GetName(), old, new)
	if _, err := c.dynClient.Cluster(lclusterName).Resource(*gvr).Namespace(ns.Name).Patch(ctx, unstr.GetName(), types.MergePatchType, clusterLabelPatchBytes(new), metav1.PatchOptions{}); err != nil {
		return err
	}
	return nil
}

func (c *Controller) reconcileGVR(ctx context.Context, gvr schema.GroupVersionResource) error {
	// Update all resources in the namespace with the cluster assignment.
	listers, _ := c.ddsif.Listers()
	lister, found := listers[gvr]
	if !found {
		return fmt.Errorf("Informer for %s is not synced; re-enqueueing", gvr)
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
	_, err = c.kubeClient.Cluster(ns.ClusterName).CoreV1().Namespaces().Patch(ctx, ns.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	if err != nil {
		return fmt.Errorf("failed to patch status on namespace %q|%q: %w", ns.ClusterName, ns.Name, err)
	}
	return nil
}

// Set the schedulable condition on namespaces and not the resources it
// contains. Not all native types support status conditions (e.g. configmaps
// and secrets) and there is no guarantee that arbitrary CRDs will support
// conditions.
func (c *Controller) updateSchedulableCondition(ctx context.Context, ns *corev1.Namespace, schedulable bool) error {
	klog.Infof("Patching namespace %q|%q status to indicate schedulable=%s", ns.ClusterName, ns.Name, schedulable)

	if schedulable {
		return c.patchStatus(ctx, ns, func(conditionsSetter conditions.Setter) {
			conditions.MarkTrue(conditionsSetter, NamespaceScheduled)
		})
	}
	return c.patchStatus(ctx, ns, func(conditionsSetter conditions.Setter) {
		conditions.MarkFalse(conditionsSetter, NamespaceScheduled, NamespaceReasonUnschedulable,
			conditionsv1alpha1.ConditionSeverityNone, // NamespaceCondition doesn't support severity
			"No clusters are available to schedule Namespaces to.")
	})
}

// assignCluster attempts to schedule a namespace to a random cluster. If no cluster is
// available, any existing cluster assignment will be cleared.
func (c *Controller) assignCluster(ctx context.Context, ns *corev1.Namespace) error {
	allClusters, err := c.clusterLister.List(labels.Everything())
	if err != nil {
		return err
	}

	var clusters []*clusterv1alpha1.Cluster
	for i := range allClusters {
		// Only include Clusters that are in the same logical cluster as ns
		if allClusters[i].ClusterName == ns.ClusterName && conditions.IsTrue(allClusters[i], clusterv1alpha1.ClusterReadyCondition) {
			clusters = append(clusters, allClusters[i])
		}
	}

	newClusterName := ""
	if len(clusters) > 0 {
		// Select a cluster at random.
		cluster := clusters[rand.Intn(len(clusters))]
		newClusterName = cluster.Name
	}

	oldClusterName := ns.Labels[clusterLabel]
	if oldClusterName != newClusterName {
		klog.Infof("Patching to update cluster assignment for namespace %q|%q: %q -> %q", ns.ClusterName, ns.Name, oldClusterName, newClusterName)
		if _, err := c.kubeClient.Cluster(ns.ClusterName).CoreV1().Namespaces().Patch(ctx, ns.Name, types.MergePatchType, clusterLabelPatchBytes(newClusterName), metav1.PatchOptions{}); err != nil {
			return err
		}

	}

	return nil
}

// isValidCluster checks whether the given cluster name exists and is valid for the
// purposes of scheduling a namespace to it.
func (c *Controller) isValidCluster(ctx context.Context, lclusterName, clusterName string) (bool, error) {
	cluster, err := c.clusterLister.Get(clusters.ToClusterAwareKey(lclusterName, clusterName))
	if apierrors.IsNotFound(err) {
		klog.Infof("Cluster %q|%q does not exist", lclusterName, clusterName)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	isReady := conditions.IsTrue(cluster, clusterv1alpha1.ClusterReadyCondition)
	if !isReady {
		klog.Infof("Cluster %q|%q is not reporting ready", lclusterName, clusterName)
	}
	return isReady, nil
}

// ensureScheduled attempts to ensure the namespace is assigned to a viable cluster. This
// will succeed without error if a cluster is assigned or if there are no viable clusters
// to assign to. The condition of not being scheduled to a cluster will be reflected in
// the namespace's status rather than by returning an error.
func (c *Controller) ensureScheduled(ctx context.Context, lclusterName string, ns *corev1.Namespace) error {
	clusterName := ns.Labels[clusterLabel]

	noClusterAssigned := clusterName == ""
	if noClusterAssigned {
		if err := c.assignCluster(ctx, ns); err != nil {
			return err
		}
	} else {
		isValidCluster, err := c.isValidCluster(ctx, lclusterName, clusterName)
		if err != nil {
			return err
		}
		if !isValidCluster {
			// Currently assigned cluster no longer exists or is not ready.
			if err := c.assignCluster(ctx, ns); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureScheduledStatus ensures the status of the given namespace reflects whether the
// namespace has been assigned to a cluster.
func (c *Controller) ensureScheduledStatus(ctx context.Context, ns *corev1.Namespace) error {
	scheduled := (ns.Labels[clusterLabel] != "")
	statusChangeRequired := scheduled != IsScheduled(ns)
	if statusChangeRequired {
		return c.updateSchedulableCondition(ctx, ns, scheduled)
	}
	return nil
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

	if err := c.ensureScheduled(ctx, lclusterName, ns); err != nil {
		return err
	}

	if err := c.ensureScheduledStatus(ctx, ns); err != nil {
		return err
	}

	// Update all resources in the namespace with the cluster assignment.
	//
	// TODO: This will happen any time a namespace update is observed,
	// including updates that don't affect cluster assignment (e.g.,
	// annotation). This will be especially painful at startup, since all
	// resources are already enqueued and reconciled separately. Add some
	// logic to filter out un-interesting updates, and consider only
	// enqueueing items here if they're not already enqueued.
	listers, notSynced := c.ddsif.Listers()
	for gvr, lister := range listers {
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

// clusterLabelPatchBytes returns JSON patch bytes expressing an operation to
// add or replace the cluster assignment label to the given value.
func clusterLabelPatchBytes(val string) []byte {
	return []byte(fmt.Sprintf(`
{
  "metadata":{
    "labels":{
      %q: %q
    }
  }
}`, clusterLabel, val))
}

// observeCluster is responsible for watching to see if the Cluster is happy;
// if it's not, any namespace assigned to that cluster will be unassigned.
//
// After the namespace is unassigned, it will be picked up by
// reconcileNamespace above and assigned to another happy cluster if one can be
// found.
func (c *Controller) observeCluster(ctx context.Context, cl *clusterv1alpha1.Cluster) error {
	klog.Infof("Observing Cluster %s", cl.Name)

	if conditions.IsTrue(cl, clusterv1alpha1.ClusterReadyCondition) {
		// Cluster's happy, nothing to do.
		return nil
	}

	// An unhealthy cluster requires revisiting the scheduling for all namespaces to
	// ensure rescheduling is performed if needed.
	return c.enqueueAllNamespaces(ctx)
}

// enqueueAllNamespaces adds all known namespaces to the queue to allow for rescheduling
// if a cluster is deleted or becomes NotReady.
func (c *Controller) enqueueAllNamespaces(ctx context.Context) error {
	namespaces, err := c.namespaceLister.ListWithContext(ctx, labels.Everything())
	if err != nil {
		return err
	}
	for _, namespace := range namespaces {
		c.enqueueNamespace(namespace)
	}

	return nil
}
