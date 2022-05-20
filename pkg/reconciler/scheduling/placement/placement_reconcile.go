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

	"github.com/kcp-dev/logicalcluster"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

type reconcileStatus int

const (
	reconcileStatusStop reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, ns *corev1.Namespace) (reconcileStatus, *corev1.Namespace, error)
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
		&statusConditionReconciler{
			patchNamespace: c.patchNamespace,
		},
	}

	var errs []error

	for _, r := range reconcilers {
		var err error
		var status reconcileStatus
		status, ns, err = r.reconcile(ctx, ns)
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
