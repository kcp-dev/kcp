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

package locationdomain

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	reconcile(ctx context.Context, domain *schedulingv1alpha1.LocationDomain) (reconcileStatus, error)
}

// locationReconciler reconciles Location objects for a LocationDomain.
type locationReconciler struct {
	listWorkloadClusters func(clusterName logicalcluster.LogicalCluster) ([]*workloadv1alpha1.WorkloadCluster, error)
	listLocations        func(clusterName logicalcluster.LogicalCluster, domainName string) ([]*schedulingv1alpha1.Location, error)
	createLocation       func(ctx context.Context, clusterName logicalcluster.LogicalCluster, location *schedulingv1alpha1.Location) (*schedulingv1alpha1.Location, error)
	updateLocation       func(ctx context.Context, clusterName logicalcluster.LogicalCluster, location *schedulingv1alpha1.Location) (*schedulingv1alpha1.Location, error)
	updateLocationStatus func(ctx context.Context, clusterName logicalcluster.LogicalCluster, location *schedulingv1alpha1.Location) (*schedulingv1alpha1.Location, error)
	deleteLocation       func(ctx context.Context, clusterName logicalcluster.LogicalCluster, name string) error
	enqueueAfter         func(logicalcluster.LogicalCluster, *schedulingv1alpha1.LocationDomain, time.Duration)
}

func (r *locationReconciler) reconcile(ctx context.Context, domain *schedulingv1alpha1.LocationDomain) (reconcileStatus, error) {
	domainClusterName := logicalcluster.From(domain)

	if domain.Spec.Instances.Workspace == nil {
		// this will never happen due to OpenAPI validation
		klog.Errorf("domain %s|%s has no workspace reference", domainClusterName, domain.Name)
		return reconcileStatusContinue, nil
	}
	negotationClusterName := logicalcluster.New(domainClusterName.Join(string(domain.Spec.Instances.Workspace.WorkspaceName)).String())

	workloadClusters, err := r.listWorkloadClusters(negotationClusterName)
	if err != nil {
		return reconcileStatusStop, err
	}

	locations, err := r.listLocations(domainClusterName, domain.Name)
	if err != nil {
		return reconcileStatusStop, err
	}

	locationsByName := map[string]*schedulingv1alpha1.Location{}
	for _, l := range locations {
		locationsByName[l.Name] = l
	}

	// update and create locations
	seenLocations := map[string]*schedulingv1alpha1.LocationDomainLocationDefinition{}
	var errs []error
	for i := range domain.Spec.Locations {
		locationDef := &domain.Spec.Locations[i]
		locationClusters, err := LocationWorkloadClusters(workloadClusters, locationDef)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		available := len(FilterReady(locationClusters))

		// intentionally after parsing the label selector
		locationName := locationDef.Name + "." + domain.Name
		seenLocations[locationName] = locationDef
		locationLabels := make(map[string]string, len(locationDef.Labels))
		sorted := make([]string, 0, len(locationDef.Labels))
		for k, v := range locationDef.Labels {
			locationLabels[string(k)] = string(v)
			sorted = append(sorted, string(k))
		}
		sort.Strings(sorted)
		labelString := ""
		for i, k := range sorted {
			if i > 0 {
				labelString += " "
			}
			labelString += fmt.Sprintf("%s=%s", k, locationLabels[k])
		}

		location := &schedulingv1alpha1.Location{
			ObjectMeta: metav1.ObjectMeta{
				Name:   locationName,
				Labels: locationLabels,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: schedulingv1alpha1.SchemeGroupVersion.String(),
						Kind:       "LocationDomain",
						Name:       domain.Name,
						UID:        domain.UID,
					},
				},
				Annotations: map[string]string{
					schedulingv1alpha1.LocationLabelsStringAnnotationKey: labelString,
				},
			},
			Spec: schedulingv1alpha1.LocationSpec{
				LocationSpecBase: locationDef.LocationSpecBase,
				Domain: schedulingv1alpha1.LocationDomainReference{
					Workspace: schedulingv1alpha1.AbsoluteWorkspaceName(domainClusterName.String()),
					Name:      domain.Name,
				},
				Type: domain.Spec.Type,
			},
		}

		old, ok := locationsByName[locationName]
		if ok {
			// first try to update
			if !equality.Semantic.DeepEqual(old.Spec, location.Spec) ||
				!equality.Semantic.DeepEqual(old.Labels, location.Labels) ||
				!equality.Semantic.DeepEqual(old.OwnerReferences, location.OwnerReferences) {

				toUpdate := old.DeepCopy()
				toUpdate.Spec = location.Spec
				toUpdate.Labels = location.Labels
				toUpdate.OwnerReferences = location.OwnerReferences

				klog.V(4).Infof("Updating location %s|%s", domainClusterName.String(), location.Name)
				if location, err = r.updateLocation(ctx, domainClusterName, toUpdate); err != nil {
					klog.Errorf("Failed to update location %s|%s: %v", domainClusterName.String(), location.Name, err)
					errs = append(errs, err)
					continue
				}
			} else {
				location = old.DeepCopy()
			}
		} else {
			klog.V(4).Infof("Creating location %s|%s", domainClusterName.String(), location.Name)
			if location, err = r.createLocation(ctx, domainClusterName, location); err != nil {
				klog.Errorf("Failed to create location %s|%s: %v", domainClusterName.String(), location.Name, err)
				errs = append(errs, err)
				continue
			}
		}

		// update status
		location.Status.Instances = uint32Ptr(uint32(len(locationClusters)))
		location.Status.AvailableInstances = uint32Ptr(uint32(available))
		if old == nil || !equality.Semantic.DeepEqual(location.Status, old.Status) {
			klog.V(4).Infof("Updating location %s|%s status", domainClusterName.String(), location.Name)
			if _, err := r.updateLocationStatus(ctx, domainClusterName, location); err != nil {
				klog.Errorf("Failed to update location %s|%s status: %v", domainClusterName.String(), location.Name, err)
				errs = append(errs, err)
			}
		}
	}

	// delete locations that are unexpected
	for i := range locations {
		location := locations[i]
		if _, ok := seenLocations[location.Name]; !ok {
			klog.V(4).Infof("Deleting location %s|%s", domainClusterName.String(), location.Name)
			if err := r.deleteLocation(ctx, domainClusterName, location.Name); err != nil {
				klog.Errorf("Failed to delete location %s|%s: %v", domainClusterName.String(), location.Name, err)
				errs = append(errs, err)
			}
		}
	}

	if len(errs) > 0 {
		return reconcileStatusStop, utilserrors.NewAggregate(errs)
	}
	return reconcileStatusContinue, nil
}

func uint32Ptr(i uint32) *uint32 {
	return &i
}

func (c *controller) reconcile(ctx context.Context, domain *schedulingv1alpha1.LocationDomain) error {
	reconcilers := []reconciler{
		&locationReconciler{
			listWorkloadClusters: c.listWorkloadCluster,
			listLocations:        c.listLocations,
			createLocation:       c.createLocation,
			updateLocation:       c.updateLocation,
			updateLocationStatus: c.updateLocationStatus,
			deleteLocation:       c.deleteLocation,
			enqueueAfter:         c.enqueueAfter,
		},
	}

	var errs []error

	for _, r := range reconcilers {
		status, err := r.reconcile(ctx, domain)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStop {
			break
		}
	}

	return utilserrors.NewAggregate(errs)
}

func (c *controller) listWorkloadCluster(clusterName logicalcluster.LogicalCluster) ([]*workloadv1alpha1.WorkloadCluster, error) {
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

func (c *controller) listLocations(clusterName logicalcluster.LogicalCluster, domainName string) ([]*schedulingv1alpha1.Location, error) {
	key := clusters.ToClusterAwareKey(clusterName, domainName)
	items, err := c.locationIndexer.ByIndex(locationsByLocationDomain, key)
	if err != nil {
		return nil, err
	}
	ret := make([]*schedulingv1alpha1.Location, 0, len(items))
	for _, item := range items {
		ret = append(ret, item.(*schedulingv1alpha1.Location))
	}
	return ret, nil
}

func (c *controller) createLocation(ctx context.Context, clusterName logicalcluster.LogicalCluster, location *schedulingv1alpha1.Location) (*schedulingv1alpha1.Location, error) {
	return c.kcpClusterClient.Cluster(clusterName).SchedulingV1alpha1().Locations().Create(ctx, location, metav1.CreateOptions{})
}

func (c *controller) updateLocation(ctx context.Context, clusterName logicalcluster.LogicalCluster, location *schedulingv1alpha1.Location) (*schedulingv1alpha1.Location, error) {
	return c.kcpClusterClient.Cluster(clusterName).SchedulingV1alpha1().Locations().Update(ctx, location, metav1.UpdateOptions{})
}

func (c *controller) updateLocationStatus(ctx context.Context, clusterName logicalcluster.LogicalCluster, location *schedulingv1alpha1.Location) (*schedulingv1alpha1.Location, error) {
	return c.kcpClusterClient.Cluster(clusterName).SchedulingV1alpha1().Locations().UpdateStatus(ctx, location, metav1.UpdateOptions{})
}

func (c *controller) deleteLocation(ctx context.Context, clusterName logicalcluster.LogicalCluster, name string) error {
	return c.kcpClusterClient.Cluster(clusterName).SchedulingV1alpha1().Locations().Delete(ctx, name, metav1.DeleteOptions{})
}
