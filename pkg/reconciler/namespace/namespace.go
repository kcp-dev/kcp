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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
)

const clusterLabel = "kcp.dev/cluster"

// reconcileResource is responsible for setting the cluster for a resource of
// any type, to match the cluster where its namespace is assigned.
func (c *Controller) reconcileResource(ctx context.Context, lclusterName string, unstr *unstructured.Unstructured, gvr *schema.GroupVersionResource) error {
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
	if _, err := c.dynClient.Resource(*gvr).Namespace(ns.Name).Patch(ctx, unstr.GetName(), types.MergePatchType, clusterLabelPatchBytes(new), metav1.PatchOptions{}); err != nil {
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

// reconcileNamespace is responsible for assigning a namespace to a cluster, if
// it does not already have one.
//
// After assigning (or if it's already assigned), this also updates all
// resources in the namespace to be assigned to the namespace's cluster.
func (c *Controller) reconcileNamespace(ctx context.Context, lclusterName string, ns *corev1.Namespace) error {
	klog.Infof("Reconciling Namespace %s", ns.Name)

	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}

	clusterName := ns.Labels[clusterLabel]
	if clusterName == "" {
		// Namespace is not assigned to a cluster; assign one.
		// First, list all clusters.
		cls, err := c.clusterLister.List(labels.Everything())
		if err != nil {
			return err
		}

		// TODO: Filter out un-Ready clusters so a namespace doesn't
		// get assigned to an un-Ready cluster.

		if len(cls) == 0 {
			// TODO set status, on namespace and all resources in namespace.
			return fmt.Errorf("no clusters")
		}

		// Select a cluster at random.
		cl := cls[rand.Intn(len(cls))]
		old, new := "", cl.Name

		// Patch the namespace.
		klog.Infof("Patching to update cluster assignment for namespace %s: %q -> %q", ns.Name, old, new)
		if _, err := c.namespaceClient.Patch(ctx, ns.Name, types.MergePatchType, clusterLabelPatchBytes(new), metav1.PatchOptions{}); err != nil {
			return err
		}
	}

	// Check if the cluster reports as Ready.
	cl, err := c.clusterLister.Get(clusters.ToClusterAwareKey(lclusterName, clusterName))
	if err != nil {
		return err
	}
	if !cl.Status.Conditions.HasReady() {
		klog.Infof("Cluster %q is not reporting as ready", cl.Name)

		// TODO: reassign this namespace to another Ready cluster at random.
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
      "kcp.dev/cluster": %q
    }
  }
}`, val))
}

// observeCluster is responsible for watching to see if the Cluster is happy;
// if it's not, any namespace assigned to that cluster will be unassigned.
//
// After the namespace is unassigned, it will be picked up by
// reconcileNamespace above and assigned to another happy cluster if one can be
// found.
func (c *Controller) observeCluster(ctx context.Context, cl *v1alpha1.Cluster) error {
	klog.Infof("Observing Cluster %s", cl.Name)

	if cl.Status.Conditions.HasReady() {
		// Cluster's happy, nothing to do.
		return nil
	}

	// TODO: If the Cluster reports as unhealthy, enqueue work items to
	// reconcile every namespace assigned to that cluster. This, along with
	// the above TODO, will reassign the namespace to a ready cluster.

	return nil
}
