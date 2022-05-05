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
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"github.com/kcp-dev/logicalcluster"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	locationreconciler "github.com/kcp-dev/kcp/pkg/reconciler/scheduling/location"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

type reconcileStatus int

const (
	reconcileStatusStop reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, ns *corev1.Namespace) (reconcileStatus, error)
}

// placementReconciler watches namespaces within a cluster workspace and assigns those to location from
// the location domain of the cluster workspace.
type placementReconciler struct {
	listAPIBindings      func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
	listLocations        func(clusterName logicalcluster.Name) ([]*schedulingv1alpha1.Location, error)
	listWorkloadClusters func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.WorkloadCluster, error)
	patchNamespace       func(ctx context.Context, clusterName logicalcluster.Name, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error)

	enqueueAfter func(logicalcluster.Name, *corev1.Namespace, time.Duration)
}

func (r *placementReconciler) reconcile(ctx context.Context, ns *corev1.Namespace) (reconcileStatus, error) {
	clusterName := logicalcluster.From(ns)
	orgClusterName, found := clusterName.Parent()
	if !found || !clusterName.HasPrefix(tenancyv1alpha1.RootCluster) {
		// ignore the root and every non-workspace.
		return reconcileStatusContinue, nil
	}
	placementValue, foundPlacement := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]

	bindings, err := r.listAPIBindings(clusterName)
	if err != nil {
		return reconcileStatusStop, err
	}
	deletePlacementAnnotation := func() (reconcileStatus, error) {
		klog.V(4).Infof("Removing placement from namespace %s|%s, no api bindings", ns.Name)
		delete(ns.Annotations, schedulingv1alpha1.PlacementAnnotationKey)
		if _, err := r.patchNamespace(ctx, clusterName, ns.Name, types.MergePatchType, []byte(fmt.Sprintf(`{"metadata":{"annotations":{%q:null}}}}`, schedulingv1alpha1.PlacementAnnotationKey)), metav1.PatchOptions{}); err != nil {
			return reconcileStatusStop, err
		}
		return reconcileStatusContinue, nil
	}
	if len(bindings) == 0 {
		if foundPlacement {
			return deletePlacementAnnotation()
		}
		return reconcileStatusContinue, nil
	}

	// find workload bindings = those that have at least one location and are ready
	var errs []error
	var workloadBindings []*apisv1alpha1.APIBinding
	locationsByWorkspace := map[logicalcluster.Name][]*schedulingv1alpha1.Location{}
	for _, binding := range bindings {
		if !conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted) || !conditions.IsTrue(binding, apisv1alpha1.APIExportValid) {
			continue
		}
		if binding.Spec.Reference.Workspace == nil {
			continue
		}
		negotationClusterName := orgClusterName.Join(binding.Spec.Reference.Workspace.WorkspaceName)
		if locations, err := r.listLocations(negotationClusterName); err != nil {
			errs = append(errs, err)
		} else if len(locations) > 0 {
			locationsByWorkspace[negotationClusterName] = locations
			workloadBindings = append(workloadBindings, binding)
		}
	}
	if len(workloadBindings) == 0 && len(errs) > 0 {
		return reconcileStatusStop, utilserrors.NewAggregate(errs)
	}
	if len(workloadBindings) == 0 {
		// TODO(sttts): we have no good way to identify workload APIBinding right now. So we use some quite high
		//              requeue duration. But better than not requeueing at all.
		r.enqueueAfter(clusterName, ns, time.Minute*2)
		return reconcileStatusContinue, nil
	}

	// sort found bindings, take the first one, just to have stability.
	// TODO(sttts): somehow avoid the situation of multiple workload bindings. Admissions?
	sort.Slice(workloadBindings, func(i, j int) bool {
		return workloadBindings[i].Name < workloadBindings[j].Name
	})
	binding := workloadBindings[0]
	negotiationClusterName := orgClusterName.Join(binding.Spec.Reference.Workspace.WorkspaceName)
	locations := locationsByWorkspace[negotiationClusterName]

	workloadClusters, err := r.listWorkloadClusters(negotiationClusterName)
	if err != nil {
		klog.Errorf("failed to list WorkloadClusters in %s for APIBinding %s|%s: %v", negotiationClusterName, clusterName, binding.Name, err)
		return reconcileStatusStop, err
	}

	perm := rand.Perm(len(locationsByWorkspace[negotiationClusterName]))
	var lastErr error
	var chosenClusters []*workloadv1alpha1.WorkloadCluster
	var chosenLocationName string
	for i := range locations {
		l := locations[perm[i]]
		locationClusters, err := locationreconciler.LocationWorkloadClusters(workloadClusters, l)
		if err != nil {
			lastErr = fmt.Errorf("failed to get location %s|%s WorkloadClusters: %w", negotiationClusterName, l.Name, err)
			continue // try another one
		}
		ready := locationreconciler.FilterReady(locationClusters)
		if len(ready) == 0 {
			continue
		}
		chosenLocationName = l.Name
		chosenClusters = ready
	}
	if chosenLocationName == "" {
		// TODO(sttts): come up with some both quicker rescheduling initially, but also some backoff when scheduling fails again
		klog.V(2).Infof("Requeuing after 30s, failed to schedule Namespace %s|%s against locations in %s. No ready clusters: %v", clusterName, ns.Name, negotiationClusterName, lastErr)
		r.enqueueAfter(clusterName, ns, time.Second*30)
		return reconcileStatusContinue, nil
	}

	// TODO(sttts): be more clever than just random: follow allocable, co-location workspace and workloads, load-balance, etcd.
	chosenCluster := chosenClusters[rand.Intn(len(chosenClusters))]

	placementUID := fmt.Sprintf("%s+%s", chosenLocationName, chosenCluster.UID)
	newPlacement := schedulingv1alpha1.PlacementAnnotation{
		placementUID: schedulingv1alpha1.PlacementStatePending,
	}

	// patch Namespace
	bs, err := json.Marshal(newPlacement)
	if err != nil {
		klog.Errorf("failed to marshal placement %v: %v", placementValue, err)
		return reconcileStatusStop, err
	}
	annotations := map[string]string{
		schedulingv1alpha1.PlacementAnnotationKey: string(bs),
	}
	bs, err = json.Marshal(annotations)
	if err != nil {
		klog.Errorf("failed to marshal placement %v: %v", placementValue, err)
		return reconcileStatusStop, err
	}

	patch := fmt.Sprintf(`{"metadata":{"annotations":%s}}`, string(bs))
	if _, err = r.patchNamespace(ctx, clusterName, ns.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return reconcileStatusStop, err
	}

	return reconcileStatusContinue, nil
}

func (c *controller) reconcile(ctx context.Context, ns *corev1.Namespace) error {
	reconcilers := []reconciler{
		&placementReconciler{
			listAPIBindings:      c.listAPIBindings,
			listLocations:        c.listLocations,
			listWorkloadClusters: c.listWorkloadClusters,
			patchNamespace:       c.patchNamespace,
			enqueueAfter:         c.enqueueAfter,
		},
	}

	var errs []error

	for _, r := range reconcilers {
		status, err := r.reconcile(ctx, ns)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStop {
			break
		}
	}

	return utilserrors.NewAggregate(errs)
}

func (c *controller) listAPIBindings(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
	items, err := c.apiBindingIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		return nil, err
	}
	ret := make([]*apisv1alpha1.APIBinding, 0, len(items))
	for _, item := range items {
		ret = append(ret, item.(*apisv1alpha1.APIBinding))
	}
	return ret, nil
}

func (c *controller) listLocations(clusterName logicalcluster.Name) ([]*schedulingv1alpha1.Location, error) {
	items, err := c.locationIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		return nil, err
	}
	ret := make([]*schedulingv1alpha1.Location, 0, len(items))
	for _, item := range items {
		ret = append(ret, item.(*schedulingv1alpha1.Location))
	}
	return ret, nil
}

func (c *controller) listWorkloadClusters(clusterName logicalcluster.Name) ([]*workloadv1alpha1.WorkloadCluster, error) {
	items, err := c.workloadClusterIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		return nil, err
	}
	ret := make([]*workloadv1alpha1.WorkloadCluster, 0, len(items))
	for _, item := range items {
		ret = append(ret, item.(*workloadv1alpha1.WorkloadCluster))
	}
	return ret, nil
}

func (c *controller) patchNamespace(ctx context.Context, clusterName logicalcluster.Name, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error) {
	return c.kubeClusterClient.Cluster(clusterName).CoreV1().Namespaces().Patch(ctx, name, pt, data, opts, subresources...)
}
