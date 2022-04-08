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
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	locationdomainreconciler "github.com/kcp-dev/kcp/pkg/reconciler/scheduling/locationdomain"
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
	getClusterWorkspace  func(clusterName logicalcluster.LogicalCluster, name string) (*tenancyv1alpha1.ClusterWorkspace, error)
	getLocationDomain    func(clusterName logicalcluster.LogicalCluster, name string) (*schedulingv1alpha1.LocationDomain, error)
	listWorkloadClusters func(clusterName logicalcluster.LogicalCluster) ([]*workloadv1alpha1.WorkloadCluster, error)
	patchNamespace       func(ctx context.Context, clusterName logicalcluster.LogicalCluster, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error)

	enqueueAfter func(logicalcluster.LogicalCluster, *corev1.Namespace, time.Duration)
}

func (r *placementReconciler) reconcile(ctx context.Context, ns *corev1.Namespace) (reconcileStatus, error) {
	clusterName := logicalcluster.From(ns)

	if _, found := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]; found {
		// this shouldn't happen by how we set up the event handlers
		return reconcileStatusContinue, nil
	}

	orgClusterName, workspaceName := clusterName.Split()
	if orgClusterName.Empty() {
		// namespace in the root cannot be placed
		klog.V(6).Infof("skip root cluster namespacee %s", ns.Name)
		return reconcileStatusContinue, nil
	}

	ws, err := r.getClusterWorkspace(orgClusterName, workspaceName)
	if err != nil {
		klog.Errorf("failed to get ClusterWorkspace %s|%s of Namespace %s|%s: %v", orgClusterName, workspaceName, clusterName, ns.Name, err)
		return reconcileStatusStop, err
	}

	key := schedulingv1alpha1.LocationDomainAssignmentLabelKeyForType(locationdomainreconciler.LocationDomainTypeWorkload)
	labelValue, found := ws.Labels[key]
	if !found || labelValue == "" {
		klog.V(6).Infof("skip unassigned ClusterWorkspace %s|%s of Namespace %s|%s", orgClusterName, workspaceName, clusterName, ns.Name)
		return reconcileStatusContinue, nil
	}
	domainClusterName, domainName := schedulingv1alpha1.ParseAssignmentLabel(labelValue)

	domain, err := r.getLocationDomain(domainClusterName, domainName)
	if apierrors.IsNotFound(err) {
		klog.V(6).Infof("requeuing Namespace %s|%s because LocationDomain %s|%s cannot be found: %v", clusterName, ns.Name, domainClusterName, domainName, err)
		return reconcileStatusStop, err
	} else if err != nil {
		klog.Errorf("failed to get LocationDomain %s|%s for ClusterWorkspace %s|%s of Namespace %s|%s: %v", domainClusterName, domainName, orgClusterName, workspaceName, clusterName, ns.Name, err)
		return reconcileStatusStop, err
	}

	if domain.Spec.Instances.Workspace == nil {
		// should never happen by validation
		klog.Errorf("LocationDomain %s|%s of ClusterWorkspace %s|%s of Namespace %s|%s has no instances Workspace", domainClusterName, domainName, orgClusterName, workspaceName, clusterName, ns.Name)
		return reconcileStatusContinue, nil
	}
	negotiationClusterName := orgClusterName.Join(string(domain.Spec.Instances.Workspace.WorkspaceName))

	workloadClusters, err := r.listWorkloadClusters(negotiationClusterName)
	if err != nil {
		klog.Errorf("failed to list WorkloadClusters in %s for LocationDomain %s|%s: %v", negotiationClusterName, domainClusterName, domain.Name, err)
		return reconcileStatusStop, err
	}

	perm := rand.Perm(len(domain.Spec.Locations))
	var lastErr error
	var chosenClusters []*workloadv1alpha1.WorkloadCluster
	var chosenLocationName string
	for i := range domain.Spec.Locations {
		locationDef := &domain.Spec.Locations[perm[i]]
		locationClusters, err := locationdomainreconciler.LocationWorkloadClusters(workloadClusters, locationDef)
		if err != nil {
			lastErr = fmt.Errorf("failed to get location %s WorkloadClusters in %s for LocationDomain %s|%s: %w", locationDef.Name, negotiationClusterName, domainClusterName, domain.Name, err)
			continue // try another one
		}
		ready := locationdomainreconciler.FilterReady(locationClusters)
		if len(ready) == 0 {
			continue
		}
		chosenLocationName = locationDef.Name
		chosenClusters = ready
	}
	if chosenLocationName == "" {
		klog.V(2).Infof("failed to schedule Namespace %s|%s against LocationDomain %s|%s. No ready clusters: %v", clusterName, ns.Name, domainClusterName, domain.Name, lastErr)
		r.enqueueAfter(clusterName, ns, time.Minute)
		return reconcileStatusContinue, nil
	}

	// TODO(sttts): be more clever than just random: follow allocable, co-location workspace and workloads, load-balance, etcd.
	chosenCluster := chosenClusters[rand.Intn(len(chosenClusters))]

	placementUID := fmt.Sprintf("%s+%s", chosenLocationName, chosenCluster.UID)
	placement := schedulingv1alpha1.PlacementAnnotation{
		placementUID: schedulingv1alpha1.PlacementStatePending,
	}

	// patch Namespace
	bs, err := json.Marshal(placement)
	if err != nil {
		klog.Errorf("failed to marshal placement %v: %v", placement, err)
		return reconcileStatusStop, err
	}
	annotations := map[string]string{
		schedulingv1alpha1.PlacementAnnotationKey: string(bs),
	}
	bs, err = json.Marshal(annotations)
	if err != nil {
		klog.Errorf("failed to marshal placement %v: %v", placement, err)
		return reconcileStatusStop, err
	}

	patch := fmt.Sprintf(`{"metadata":{"annotation":%s}}`, string(bs))
	if _, err = r.patchNamespace(ctx, clusterName, ns.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return reconcileStatusStop, err
	}

	return reconcileStatusContinue, nil
}

func (c *controller) reconcile(ctx context.Context, ns *corev1.Namespace) error {
	reconcilers := []reconciler{
		&placementReconciler{
			getClusterWorkspace:  c.getClusterWorkspace,
			getLocationDomain:    c.getLocationDomain,
			listWorkloadClusters: c.listWorkloadClusters,
			patchNamespace:       c.patchNamespace,

			enqueueAfter: c.enqueueAfter,
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

func (c *controller) getClusterWorkspace(clusterName logicalcluster.LogicalCluster, name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
	return c.clusterWorkspaceLister.Get(clusters.ToClusterAwareKey(clusterName, name))
}

func (c *controller) getLocationDomain(clusterName logicalcluster.LogicalCluster, name string) (*schedulingv1alpha1.LocationDomain, error) {
	return c.locationDomainLister.Get(clusters.ToClusterAwareKey(clusterName, name))
}

func (c *controller) listWorkloadClusters(clusterName logicalcluster.LogicalCluster) ([]*workloadv1alpha1.WorkloadCluster, error) {
	items, err := c.workloadClusterIndexer.ByIndex(workloadClustersByWorkspace, clusterName.String())
	if err != nil {
		return nil, err
	}
	ret := make([]*workloadv1alpha1.WorkloadCluster, 0, len(items))
	for _, item := range items {
		ret = append(ret, item.(*workloadv1alpha1.WorkloadCluster))
	}
	return ret, nil
}

func (c *controller) patchNamespace(ctx context.Context, clusterName logicalcluster.LogicalCluster, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*corev1.Namespace, error) {
	return c.kubeClusterClient.Cluster(clusterName).CoreV1().Namespaces().Patch(ctx, name, pt, data, opts, subresources...)
}
