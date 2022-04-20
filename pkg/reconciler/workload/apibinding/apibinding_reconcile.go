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

package apibinding

import (
	"context"
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexport"
)

type reconcileStatus int

const (
	reconcileStatusStop reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, ws *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error)
}

type apibindingReconciler struct {
	getLocationDomain func(clusterName logicalcluster.LogicalCluster, name string) (*schedulingv1alpha1.LocationDomain, error)
	createAPIBinding  func(ctx context.Context, clusterName logicalcluster.LogicalCluster, binding *apisv1alpha1.APIBinding) (*apisv1alpha1.APIBinding, error)

	enqueueAfter func(*apisv1alpha1.APIBinding, time.Duration)
}

func (r *apibindingReconciler) reconcile(ctx context.Context, ws *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error) {
	clusterName := logicalcluster.From(ws).Join(ws.Name)

	key := schedulingv1alpha1.LocationDomainAssignmentLabelKeyForType("Workloads")
	value := ws.Labels[key]
	if len(value) == 0 {
		// nothing to do. Shouldn't happen as we have filtered event handlers.
		return reconcileStatusContinue, nil
	}

	locationWorkspace, locationName := schedulingv1alpha1.ParseAssignmentLabel(value)
	locationDomain, err := r.getLocationDomain(locationWorkspace, locationName)
	if apierrors.IsNotFound(err) {
		// nothing we can do to fix this here
		klog.Warningf("LocationDomain %s|%s not found referenced by ClusterWorkspace %s", locationWorkspace, locationName, clusterName)
		return reconcileStatusContinue, nil
	} else if err != nil {
		return reconcileStatusStop, err
	}

	if locationDomain.Spec.Instances.Workspace == nil {
		return reconcileStatusContinue, nil
	}

	_, err = r.createAPIBinding(ctx, clusterName, &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: apiexport.DefaultAPIExportName, // named the same way
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					WorkspaceName: string(locationDomain.Spec.Instances.Workspace.WorkspaceName),
					ExportName:    apiexport.DefaultAPIExportName,
				},
			},
		},
	})
	if apierrors.IsAlreadyExists(err) {
		// nothing to do
		return reconcileStatusContinue, nil
	} else if err != nil {
		klog.Errorf("Failed to create APIBinding %s|%s: %v", clusterName, apiexport.DefaultAPIExportName, err)
		return reconcileStatusStop, err
	}

	return reconcileStatusContinue, nil
}

func (c *controller) reconcile(ctx context.Context, ws *tenancyv1alpha1.ClusterWorkspace) error {
	reconcilers := []reconciler{
		&apibindingReconciler{
			getLocationDomain: c.getLocationDomain,
			createAPIBinding:  c.createAPIBinding,
			enqueueAfter:      c.enqueueAfter,
		},
	}

	var errs []error

	for _, r := range reconcilers {
		status, err := r.reconcile(ctx, ws)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStop {
			break
		}
	}

	return errors.NewAggregate(errs)
}

func (c *controller) getLocationDomain(clusterName logicalcluster.LogicalCluster, name string) (*schedulingv1alpha1.LocationDomain, error) {
	return c.locationDomainLister.Get(clusters.ToClusterAwareKey(clusterName, name))
}

func (c *controller) createAPIBinding(ctx context.Context, clusterName logicalcluster.LogicalCluster, binding *apisv1alpha1.APIBinding) (*apisv1alpha1.APIBinding, error) {
	return c.kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
}
