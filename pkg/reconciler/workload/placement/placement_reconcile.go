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

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

type reconcileStatus int

const (
	reconcileStatusStop reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, placement *schedulingv1alpha1.Placement) (reconcileStatus, *schedulingv1alpha1.Placement, error)
}

func (c *controller) reconcile(ctx context.Context, placement *schedulingv1alpha1.Placement) error {
	reconcilers := []reconciler{
		&placementSchedulingReconciler{
			listSyncTarget: c.listSyncTarget,
			getLocation:    c.getLocation,
			patchPlacement: c.patchPlacement,
		},
	}

	var errs []error

	for _, r := range reconcilers {
		var err error
		var status reconcileStatus
		status, placement, err = r.reconcile(ctx, placement)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStop {
			break
		}
	}

	return utilserrors.NewAggregate(errs)
}

func (c *controller) listSyncTarget(clusterName logicalcluster.Name) ([]*workloadv1alpha1.SyncTarget, error) {
	items, err := c.syncTargetIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		return nil, err
	}
	ret := make([]*workloadv1alpha1.SyncTarget, 0, len(items))
	for _, item := range items {
		ret = append(ret, item.(*workloadv1alpha1.SyncTarget))
	}
	return ret, nil
}

func (c *controller) getLocation(clusterName logicalcluster.Name, name string) (*schedulingv1alpha1.Location, error) {
	key := clusters.ToClusterAwareKey(clusterName, name)
	return c.locationLister.Get(key)
}

func (c *controller) patchPlacement(ctx context.Context, clusterName logicalcluster.Name, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*schedulingv1alpha1.Placement, error) {
	logger := klog.FromContext(ctx)
	logger.WithValues("patch", string(data)).V(2).Info("patching Placement")
	return c.kcpClusterClient.SchedulingV1alpha1().Placements().Patch(logicalcluster.WithCluster(ctx, clusterName), name, pt, data, opts, subresources...)
}
