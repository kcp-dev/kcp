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
	"fmt"
	"testing"
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

type BindingCheck func(t *testing.T, b *apisv1alpha1.APIBinding)

func equals(expected *apisv1alpha1.APIBinding) func(t *testing.T, b *apisv1alpha1.APIBinding) {
	return func(t *testing.T, got *apisv1alpha1.APIBinding) {
		t.Helper()
		require.Equal(t, expected, got)
	}
}

func TestPlacementReconciler(t *testing.T) {
	tests := map[string]struct {
		domains          map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain
		clusterWorkspace *tenancyv1alpha1.ClusterWorkspace

		getLocationDomainError error
		createAPIBindingError  error

		wantCreates map[string]BindingCheck

		wantReconcileStatus reconcileStatus
		wantRequeue         time.Duration
		wantError           bool
	}{
		"no assignment": {
			clusterWorkspace: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "ws",
					ClusterName: "root:org",
				},
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"assigned, but missing": {
			clusterWorkspace: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "ws",
					ClusterName: "root:org",
					Labels: map[string]string{
						"scheduling.kcp.dev/workloads-location-domain": "root.org.standard-compute",
					},
				},
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"assigned, but error": {
			clusterWorkspace: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "ws",
					ClusterName: "root:org",
					Labels: map[string]string{
						"scheduling.kcp.dev/workloads-location-domain": "root.org.standard-compute",
					},
				},
			},
			getLocationDomainError: fmt.Errorf("boom"),
			wantReconcileStatus:    reconcileStatusStop,
			wantError:              true,
		},
		"happy-path: assigned and existing domain": {
			clusterWorkspace: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "ws",
					ClusterName: "root:org",
					Labels: map[string]string{
						"scheduling.kcp.dev/workloads-location-domain": "root.org.standard-compute",
					},
				},
			},
			domains: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain{
				logicalcluster.New("root:org"): {
					domain(logicalcluster.New("root:org"), "standard-compute", "Workloads"),
				},
			},
			wantCreates: map[string]BindingCheck{
				"workloads": equals(&apisv1alpha1.APIBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name: "workloads",
					},
					Spec: apisv1alpha1.APIBindingSpec{
						Reference: apisv1alpha1.ExportReference{
							Workspace: &apisv1alpha1.WorkspaceExportReference{
								ExportName:    "workloads",
								WorkspaceName: "negotiation-workspace",
							},
						},
					},
				}),
			},
			wantReconcileStatus: reconcileStatusContinue,
		},
		"happy-path: assigned and existing domain, already exists": {
			clusterWorkspace: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "ws",
					ClusterName: "root:org",
					Labels: map[string]string{
						"scheduling.kcp.dev/workloads-location-domain": "root.org.standard-compute",
					},
				},
			},
			domains: map[logicalcluster.LogicalCluster][]*schedulingv1alpha1.LocationDomain{
				logicalcluster.New("root:org"): {
					domain(logicalcluster.New("root:org"), "standard-compute", "Workloads"),
				},
			},
			createAPIBindingError: apierrors.NewAlreadyExists(schema.GroupResource{Group: "apis.kcp.dev", Resource: "apibindings"}, "workloads"),
			wantReconcileStatus:   reconcileStatusContinue,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var requeuedAfter time.Duration
			creates := map[string]*apisv1alpha1.APIBinding{}
			r := &apibindingReconciler{
				getLocationDomain: func(clusterName logicalcluster.LogicalCluster, name string) (*schedulingv1alpha1.LocationDomain, error) {
					if tc.getLocationDomainError != nil {
						return nil, tc.getLocationDomainError
					}
					for _, l := range tc.domains[clusterName] {
						if l.Name == name {
							return l, nil
						}
					}
					return nil, apierrors.NewNotFound(schema.GroupResource{Group: "scheduling.kcp.dev", Resource: "locationdomains"}, name)
				},
				createAPIBinding: func(ctx context.Context, clusterName logicalcluster.LogicalCluster, binding *apisv1alpha1.APIBinding) (*apisv1alpha1.APIBinding, error) {
					if tc.createAPIBindingError != nil {
						return nil, tc.createAPIBindingError
					}
					creates[binding.Name] = binding.DeepCopy()
					return binding, nil
				},
				enqueueAfter: func(binding *apisv1alpha1.APIBinding, duration time.Duration) {
					requeuedAfter = duration
				},
			}

			status, err := r.reconcile(context.Background(), tc.clusterWorkspace)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, status, tc.wantReconcileStatus)
			require.Equal(t, tc.wantRequeue, requeuedAfter)

			// check creates
			for _, l := range creates {
				require.Contains(t, tc.wantCreates, l.Name, "got unexpected create:\n%s", toYaml(l))
				tc.wantCreates[l.Name](t, l)
			}
			for name := range tc.wantCreates {
				require.Contains(t, creates, name, "missing create of %s", name)
			}
		})
	}
}

func domain(clusterName logicalcluster.LogicalCluster, name string, domainType string) *schedulingv1alpha1.LocationDomain {
	return &schedulingv1alpha1.LocationDomain{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			ClusterName: clusterName.String(),
		},
		Spec: schedulingv1alpha1.LocationDomainSpec{
			Type: schedulingv1alpha1.LocationDomainType(domainType),
			Instances: schedulingv1alpha1.InstancesReference{
				Resource: schedulingv1alpha1.GroupVersionResource{
					Group:    "workload.kcp.dev",
					Version:  "v1alpha1",
					Resource: "workloadclusters",
				},
				Workspace: &schedulingv1alpha1.WorkspaceExportReference{
					WorkspaceName: "negotiation-workspace",
				},
			},
		},
	}
}

func toYaml(obj interface{}) string {
	bytes, err := yaml.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return string(bytes)
}
