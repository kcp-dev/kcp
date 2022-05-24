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

package resource

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kcp-dev/logicalcluster"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

// reconcileResource is responsible for setting the cluster for a resource of
// any type, to match the cluster where its namespace is assigned.
func (c *Controller) reconcileResource(ctx context.Context, lclusterName logicalcluster.Name, unstr *unstructured.Unstructured, gvr *schema.GroupVersionResource) error {
	if gvr.Group == "networking.k8s.io" && gvr.Resource == "ingresses" {
		klog.V(4).Infof("Skipping reconciliation of ingress %s/%s", unstr.GetNamespace(), unstr.GetName())
		return nil
	}

	klog.V(2).Infof("Reconciling GVR %q %s|%s/%s", gvr.String(), lclusterName, unstr.GetNamespace(), unstr.GetName())

	// If the resource is not namespaced (incl if the resource is itself a
	// namespace), ignore it.
	if unstr.GetNamespace() == "" {
		klog.V(4).Infof("GVR %q %s|%s had no namespace; ignoring", gvr.String(), unstr.GetClusterName(), unstr.GetName())
		return nil
	}

	// Align the resource's assigned cluster with the namespace's assigned
	// cluster.
	// First, get the namespace object (from the cached lister).
	ns, err := c.namespaceLister.Get(clusters.ToClusterAwareKey(lclusterName, unstr.GetNamespace()))
	if apierrors.IsNotFound(err) {
		// Namespace was deleted; this resource will eventually get deleted too, so ignore
		return nil
	}
	if err != nil {
		return fmt.Errorf("error reconciling resource %s|%s/%s: error getting namespace: %w", lclusterName, unstr.GetNamespace(), unstr.GetName(), err)
	}

	lbls := unstr.GetLabels()
	if lbls == nil {
		lbls = map[string]string{}
	}
	//nolint:staticcheck
	previousCluster, newCluster := shared.DeprecatedGetAssignedWorkloadCluster(lbls), shared.DeprecatedGetAssignedWorkloadCluster(ns.Labels)
	if previousCluster == newCluster {
		// Already assigned to the right cluster.
		return nil
	}

	// TODO(ncdc): wildcard partial metadata requests are now including CRs that come via APIBindings. Multiple e2e tests
	// manually assign a scheduling label to a resource but not its namespace. The resources were getting synced based
	// on the manually assigned label, and staying with that assignment. But now because wildcard partial metadata
	// lists include these bound CRs, not having the namespace assigned is a problem. For now, don't unassign a
	// resource that is scheduled if the namespace's assignment is unset.
	if previousCluster != "" && newCluster == "" {
		return nil
	}

	// Update the resource's assignment.
	patchType, patchBytes, err := clusterLabelPatchBytes(previousCluster, newCluster)
	if err != nil {
		klog.Errorf("error creating patch for %s %s|%s: %v", gvr.String(), unstr.GetClusterName(), unstr.GetName(), err)
		return err
	}

	if updated, err := c.dynClient.Cluster(lclusterName).Resource(*gvr).Namespace(ns.Name).
		Patch(ctx, unstr.GetName(), patchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return err
	} else {
		klog.V(2).Infof("Patched cluster assignment for %q %s|%s/%s: %q -> %q. Labels=%v",
			gvr, lclusterName, ns.Name, unstr.GetName(), previousCluster, newCluster, updated.GetLabels())
	}
	return nil
}

func (c *Controller) reconcileGVR(gvr schema.GroupVersionResource) error {
	// Update all resources in the namespace with the cluster assignment.
	listers, _ := c.ddsif.Listers()
	lister, found := listers[gvr]
	if !found {
		return fmt.Errorf("informer for %q is not synced; re-enqueueing", gvr)
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

// clusterLabelPatchBytes returns a patch expressing an operation
// to add, replace to the given value, or delete the cluster assignment label on
// a resource.
func clusterLabelPatchBytes(old, new string) (types.PatchType, []byte, error) {
	patches := make(map[string]interface{})

	if new == "" && old != "" {
		patches[workloadv1alpha1.InternalClusterResourceStateLabelPrefix+old] = nil
	} else if new != "" && old == "" {
		patches[workloadv1alpha1.InternalClusterResourceStateLabelPrefix+new] = string(workloadv1alpha1.ResourceStateSync)
	} else {
		patches[workloadv1alpha1.InternalClusterResourceStateLabelPrefix+old] = nil
		patches[workloadv1alpha1.InternalClusterResourceStateLabelPrefix+new] = string(workloadv1alpha1.ResourceStateSync)
	}

	bs, err := json.Marshal(map[string]interface{}{"metadata": map[string]interface{}{"labels": patches}})
	if err != nil {
		return "", nil, err
	}
	return types.MergePatchType, bs, nil
}
