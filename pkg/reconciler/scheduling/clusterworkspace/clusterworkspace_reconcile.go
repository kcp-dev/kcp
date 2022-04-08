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

package clusterworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

type reconcileStatus int

const (
	reconcileStatusStop reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, cluster *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error)
}

// domainAssignmentReconciler assigns a LocationDomains to a ClusterWorkspace via labels.
type domainAssignmentReconciler struct {
	listLocationDomains   func(clusterName logicalcluster.LogicalCluster) ([]*schedulingv1alpha1.LocationDomain, error)
	patchClusterWorkspace func(ctx context.Context, clusterName logicalcluster.LogicalCluster, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*tenancyv1alpha1.ClusterWorkspace, error)
	enqueueAfter          func(logicalcluster.LogicalCluster, *tenancyv1alpha1.ClusterWorkspace, time.Duration)
}

func (r *domainAssignmentReconciler) reconcile(ctx context.Context, cluster *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error) {
	orgClusterName := logicalcluster.From(cluster)

	// get domains up to the root
	matchingDomainsByDomainType := map[schedulingv1alpha1.LocationDomainType][]*schedulingv1alpha1.LocationDomain{}
	for clusterName := orgClusterName; !clusterName.Empty(); clusterName, _ = clusterName.Split() {
		domains, err := r.listLocationDomains(clusterName)
		if err != nil {
			return reconcileStatusStop, err
		}
		for _, domain := range domains {
			domainType := domain.Spec.Type

			if domain.Spec.WorkspaceSelector == nil {
				continue
			}

			for _, workspaceType := range domain.Spec.WorkspaceSelector.Types {
				if string(workspaceType) == cluster.Spec.Type {
					matchingDomainsByDomainType[domainType] = append(matchingDomainsByDomainType[domainType], domain)
					break
				}
			}
		}
	}

	// TODO(sttts): having to list all domains to get the types in order to check for the label is not nice
	patchLabels := map[string]string{}
	for t, domains := range matchingDomainsByDomainType {
		key := schedulingv1alpha1.LocationDomainAssignmentLabelKeyForType(t)
		if _, found := cluster.Labels[key]; found {
			// don't touch existing assignments
			// TODO(sttts): maybe check if the assignment is still valid?
			continue
		}

		sort.Slice(domains, func(i, j int) bool {
			return domains[i].Spec.WorkspaceSelector.Priority > domains[j].Spec.WorkspaceSelector.Priority
		})

		chosen := domains[0]
		patchLabels[key] = schedulingv1alpha1.LocationDomainAssignmentLabelValue(logicalcluster.From(chosen), chosen.Name)
	}

	// patch ClusterWorkspace if necessary
	// Note: we cannot just let the outer controller code update the label as CR labels cannot be set via the UpdateStatus method.
	if len(patchLabels) > 0 {
		bs, err := json.Marshal(patchLabels)
		if err != nil {
			return reconcileStatusStop, err
		}
		patch := fmt.Sprintf(`{"metadata":{"labels":%s}}`, string(bs))
		if _, err = r.patchClusterWorkspace(ctx, orgClusterName, cluster.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
			return reconcileStatusStop, err
		}
	}

	return reconcileStatusContinue, nil
}

func (c *controller) reconcile(ctx context.Context, cluster *tenancyv1alpha1.ClusterWorkspace) error {
	reconcilers := []reconciler{
		&domainAssignmentReconciler{
			listLocationDomains:   c.listLocationDomains,
			patchClusterWorkspace: c.patchClusterWorkspace,
			enqueueAfter:          c.enqueueAfter,
		},
	}

	var errs []error

	for _, r := range reconcilers {
		status, err := r.reconcile(ctx, cluster)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStop {
			break
		}
	}

	return utilserrors.NewAggregate(errs)
}

func (c *controller) listLocationDomains(clusterName logicalcluster.LogicalCluster) ([]*schedulingv1alpha1.LocationDomain, error) {
	items, err := c.locationDomainIndexer.ByIndex(locationDomainByWorkspace, clusterName.String())
	if err != nil {
		return nil, err
	}
	ret := make([]*schedulingv1alpha1.LocationDomain, 0, len(items))
	for _, item := range items {
		ret = append(ret, item.(*schedulingv1alpha1.LocationDomain))
	}
	return ret, nil
}

func (c *controller) patchClusterWorkspace(ctx context.Context, clusterName logicalcluster.LogicalCluster, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*tenancyv1alpha1.ClusterWorkspace, error) {
	return c.kcpClusterClient.Cluster(clusterName).TenancyV1alpha1().ClusterWorkspaces().Patch(ctx, name, pt, data, opts, subresources...)
}
