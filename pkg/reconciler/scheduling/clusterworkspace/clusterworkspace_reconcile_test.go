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
	"fmt"
	"testing"
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func TestDomainAssignmentReconciler(t *testing.T) {
	tests := map[string]struct {
		domains             map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain
		clusterLabels       map[string]string
		listError           error
		patchError          error
		wantReconcileStatus reconcileStatus
		wantPatch           string
		wantRequeue         time.Duration
		wantError           bool
	}{
		"no domains, no label": {
			domains: nil,
			clusterLabels: map[string]string{
				"foo": "bar",
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"no domains, existing label": {
			domains: nil,
			clusterLabels: map[string]string{
				"foo": "bar",
				"scheduling.kcp.dev/apples-location-domain": "boskop",
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"potato domain without workspace selector, no label": {
			domains: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain{
				logicalcluster.New("root:org"): {
					domain("potatoes", "Potatoes"),
				},
			},
			clusterLabels: map[string]string{
				"foo": "bar",
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"potato domain with workspace selector, no label": {
			domains: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain{
				logicalcluster.New("root:org"): {
					withWorkspaceSelector(0, []string{"Universal"}, domain("potatoes", "Potatoes")),
				},
			},
			clusterLabels: map[string]string{
				"foo": "bar",
			},
			wantReconcileStatus: reconcileStatusContinue,
			wantPatch:           `{"metadata":{"labels":{"scheduling.kcp.dev/potatoes-location-domain":"root.org.potatoes"}}}`,
		},
		"multiple domain types with workspace selector, no label": {
			domains: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain{
				logicalcluster.New("root:org"): {
					withWorkspaceSelector(0, []string{"Universal"}, domain("potatoes", "Potatoes")),
					withWorkspaceSelector(0, []string{"Universal"}, domain("apples", "Apples")),
				},
			},
			clusterLabels: map[string]string{
				"foo": "bar",
			},
			wantReconcileStatus: reconcileStatusContinue,
			wantPatch:           "{\"metadata\":{\"labels\":{\"scheduling.kcp.dev/apples-location-domain\":\"root.org.apples\",\"scheduling.kcp.dev/potatoes-location-domain\":\"root.org.potatoes\"}}}",
		},
		"multiple domains, higher prio in root, no label": {
			domains: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain{
				logicalcluster.New("root:org"): {
					withWorkspaceSelector(3, []string{"Universal"}, domain("potatoes", "Potatoes")),
				},
				logicalcluster.New("root"): {
					withWorkspaceSelector(5, []string{"Universal"}, domain("potatoes", "Potatoes")),
				},
			},
			clusterLabels: map[string]string{
				"foo": "bar",
			},
			wantReconcileStatus: reconcileStatusContinue,
			wantPatch:           "{\"metadata\":{\"labels\":{\"scheduling.kcp.dev/potatoes-location-domain\":\"root.potatoes\"}}}",
		},
		"multiple domains, higher prio in the org, no label": {
			domains: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain{
				logicalcluster.New("root:org:ws"): {
					withWorkspaceSelector(1, []string{"Universal"}, domain("potatoes", "Potatoes")),
				},
				logicalcluster.New("root:org"): {
					withWorkspaceSelector(3, []string{"Universal"}, domain("potatoes", "Potatoes")),
				},
				logicalcluster.New("root"): {
					withWorkspaceSelector(2, []string{"Universal"}, domain("potatoes", "Potatoes")),
				},
			},
			clusterLabels: map[string]string{
				"foo": "bar",
			},
			wantReconcileStatus: reconcileStatusContinue,
			wantPatch:           "{\"metadata\":{\"labels\":{\"scheduling.kcp.dev/potatoes-location-domain\":\"root.org.potatoes\"}}}",
		},
		"pre-existing label": {
			domains: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain{
				logicalcluster.New("root:org"): {
					withWorkspaceSelector(3, []string{"Universal"}, domain("potatoes", "Potatoes")),
				},
			},
			clusterLabels: map[string]string{
				"foo": "bar",
				"scheduling.kcp.dev/potatoes-location-domain": "root.org.blub",
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"domain in the workspace itself": {
			domains: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain{
				logicalcluster.New("root:org"): {
					withWorkspaceSelector(3, []string{"Universal"}, domain("potatoes", "Potatoes")),
				},
				logicalcluster.New("root:org:ws"): {
					withWorkspaceSelector(5, []string{"Universal"}, domain("potatoes", "Potatoes")),
				},
			},
			clusterLabels: map[string]string{
				"foo": "bar",
			},
			wantPatch:           "{\"metadata\":{\"labels\":{\"scheduling.kcp.dev/potatoes-location-domain\":\"root.org.potatoes\"}}}",
			wantReconcileStatus: reconcileStatusContinue,
		},
		"list error": {
			domains: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain{
				logicalcluster.New("root:org"): {
					withWorkspaceSelector(3, []string{"Universal"}, domain("potatoes", "Potatoes")),
				},
			},
			clusterLabels: map[string]string{
				"foo": "bar",
			},
			listError:           fmt.Errorf("boom"),
			wantReconcileStatus: reconcileStatusStop,
			wantError:           true,
		},
		"patch error": {
			domains: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain{
				logicalcluster.New("root:org"): {
					withWorkspaceSelector(3, []string{"Universal"}, domain("potatoes", "Potatoes")),
				},
			},
			clusterLabels: map[string]string{
				"foo": "bar",
			},
			patchError:          fmt.Errorf("boom"),
			wantReconcileStatus: reconcileStatusStop,
			wantError:           true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var requeuedAfter time.Duration
			var gotPatch string
			r := &domainAssignmentReconciler{
				listLocationDomains: func(cluster logicalcluster.LogicalCluster) ([]*schedulingv1alpha1.LocationDomain, error) {
					if tc.listError != nil {
						return nil, tc.listError
					}
					ret := make([]*schedulingv1alpha1.LocationDomain, 0, len(tc.domains[cluster]))
					for _, d := range tc.domains[cluster] {
						clone := d.DeepCopy()
						clone.ClusterName = cluster.String()
						ret = append(ret, clone)
					}
					return ret, nil
				},
				patchClusterWorkspace: func(ctx context.Context, clusterName logicalcluster.LogicalCluster, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*tenancyv1alpha1.ClusterWorkspace, error) {
					if tc.patchError != nil {
						return nil, tc.patchError
					}
					gotPatch = string(data)
					return &tenancyv1alpha1.ClusterWorkspace{}, nil // we don't care about the actual result in the reconciler
				},
				enqueueAfter: func(clusterName logicalcluster.LogicalCluster, cluster *tenancyv1alpha1.ClusterWorkspace, duration time.Duration) {
					requeuedAfter = duration
				},
			}

			status, err := r.reconcile(context.Background(), &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "ws",
					ClusterName: "root:org",
					Labels:      tc.clusterLabels,
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					Type: "Universal",
				},
			})
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, status, tc.wantReconcileStatus)
			require.Equal(t, tc.wantRequeue, requeuedAfter)
			require.Equal(t, tc.wantPatch, gotPatch)
		})
	}
}

func domain(name string, domainType string) *schedulingv1alpha1.LocationDomain {
	return &schedulingv1alpha1.LocationDomain{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: schedulingv1alpha1.LocationDomainSpec{
			Type: schedulingv1alpha1.LocationDomainType(domainType),
		},
	}
}

func withWorkspaceSelector(prio int32, types []string, domain *schedulingv1alpha1.LocationDomain) *schedulingv1alpha1.LocationDomain {
	wsTypes := make([]schedulingv1alpha1.WorkspaceType, 0, len(types))
	for _, t := range types {
		wsTypes = append(wsTypes, schedulingv1alpha1.WorkspaceType(t))
	}
	domain.Spec.WorkspaceSelector = &schedulingv1alpha1.WorkspaceSelector{
		Types:    wsTypes,
		Priority: prio,
	}
	return domain
}
