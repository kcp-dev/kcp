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

package location

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

type reconcileStatus int

const (
	reconcileStatusStop reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, location *schedulingv1alpha1.Location) (reconcileStatus, error)
}

// statusReconciler reconciles Location objects' status.
type statusReconciler struct {
	listSyncTargets func(clusterName logicalcluster.Name) ([]*workloadv1alpha1.SyncTarget, error)
	updateLocation  func(ctx context.Context, clusterName logicalcluster.Name, location *schedulingv1alpha1.Location) (*schedulingv1alpha1.Location, error)
	enqueueAfter    func(*schedulingv1alpha1.Location, time.Duration)
}

func (r *statusReconciler) reconcile(ctx context.Context, location *schedulingv1alpha1.Location) (reconcileStatus, error) {
	clusterName := logicalcluster.From(location)
	syncTargets, err := r.listSyncTargets(clusterName)
	if err != nil {
		return reconcileStatusStop, err
	}

	// compute label string to be used in a table column
	locationLabels := make(map[string]string, len(location.Labels))
	sorted := make([]string, 0, len(location.Labels))
	for k, v := range location.Labels {
		locationLabels[k] = v
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	labelString := ""
	for i, k := range sorted {
		if i > 0 {
			labelString += " "
		}
		labelString += fmt.Sprintf("%s=%s", k, locationLabels[k])
	}
	if labelString != location.Annotations[schedulingv1alpha1.LocationLabelsStringAnnotationKey] {
		if location.Annotations == nil {
			location.Annotations = make(map[string]string, 1)
		}
		location.Annotations[schedulingv1alpha1.LocationLabelsStringAnnotationKey] = labelString
		if location, err = r.updateLocation(ctx, clusterName, location); err != nil {
			return reconcileStatusStop, err
		}
	}

	// update status
	locationClusters, err := LocationSyncTargets(syncTargets, location)
	if err != nil {
		return reconcileStatusStop, err
	}
	available := len(FilterReady(locationClusters))
	location.Status.Instances = uint32Ptr(uint32(len(locationClusters)))
	location.Status.AvailableInstances = uint32Ptr(uint32(available))

	return reconcileStatusContinue, nil
}

func uint32Ptr(i uint32) *uint32 {
	return &i
}

func (c *controller) reconcile(ctx context.Context, location *schedulingv1alpha1.Location) error {
	reconcilers := []reconciler{
		&statusReconciler{
			listSyncTargets: c.listSyncTarget,
			updateLocation:  c.updateLocation,
			enqueueAfter:    c.enqueueAfter,
		},
	}

	var errs []error

	for _, r := range reconcilers {
		status, err := r.reconcile(ctx, location)
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

func (c *controller) updateLocation(ctx context.Context, clusterName logicalcluster.Name, location *schedulingv1alpha1.Location) (*schedulingv1alpha1.Location, error) {
	return c.kcpClusterClient.SchedulingV1alpha1().Locations().Update(logicalcluster.WithCluster(ctx, clusterName), location, metav1.UpdateOptions{})
}
