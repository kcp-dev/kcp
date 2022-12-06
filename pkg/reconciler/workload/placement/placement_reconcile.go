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

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

type reconcileStatus int

const (
	reconcileStatusStopAndRequeue reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, placement *schedulingv1alpha1.Placement) (reconcileStatus, *schedulingv1alpha1.Placement, error)
}

func (c *controller) reconcile(ctx context.Context, placement *schedulingv1alpha1.Placement) (bool, error) {
	reconcilers := []reconciler{
		&placementSchedulingReconciler{
			listSyncTarget:          c.listSyncTarget,
			getLocation:             c.getLocation,
			patchPlacement:          c.patchPlacement,
			listWorkloadAPIBindings: c.listWorkloadAPIBindings,
		},
	}

	var errs []error

	requeue := false
	for _, r := range reconcilers {
		var err error
		var status reconcileStatus
		status, placement, err = r.reconcile(ctx, placement)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStopAndRequeue {
			requeue = true
			break
		}
	}

	return requeue, utilserrors.NewAggregate(errs)
}

func (c *controller) listSyncTarget(clusterName logicalcluster.Path) ([]*workloadv1alpha1.SyncTarget, error) {
	return c.syncTargetLister.Cluster(clusterName).List(labels.Everything())
}

func (c *controller) getLocation(clusterName logicalcluster.Path, name string) (*schedulingv1alpha1.Location, error) {
	return c.locationLister.Cluster(clusterName).Get(name)
}

func (c *controller) patchPlacement(ctx context.Context, clusterName logicalcluster.Path, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*schedulingv1alpha1.Placement, error) {
	logger := klog.FromContext(ctx)
	logger.WithValues("patch", string(data)).V(2).Info("patching Placement")
	return c.kcpClusterClient.Cluster(clusterName).SchedulingV1alpha1().Placements().Patch(ctx, name, pt, data, opts, subresources...)
}

// listWorkloadAPIBindings list all compute apibindings
func (c *controller) listWorkloadAPIBindings(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
	apiBindings, err := c.apiBindingLister.Cluster(clusterName).List(labels.Everything())
	if err != nil {
		return nil, err
	}

	var filteredAPIBinding []*apisv1alpha1.APIBinding
	for _, apiBinding := range apiBindings {
		if _, ok := apiBinding.Annotations[workloadv1alpha1.ComputeAPIExportAnnotationKey]; ok {
			filteredAPIBinding = append(filteredAPIBinding, apiBinding)
		}
	}
	return filteredAPIBinding, nil
}
