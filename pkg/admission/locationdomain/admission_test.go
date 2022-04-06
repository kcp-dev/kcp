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
	"testing"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
)

func createAttr(ws *schedulingv1alpha1.LocationDomain) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(ws),
		nil,
		schedulingv1alpha1.Kind("LocationDomains").WithVersion("v1alpha1"),
		"",
		ws.Name,
		schedulingv1alpha1.Resource("locationdomains").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func updateAttr(ws, old *schedulingv1alpha1.LocationDomain) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(ws),
		helpers.ToUnstructuredOrDie(old),
		schedulingv1alpha1.Kind("LocationDomains").WithVersion("v1alpha1"),
		"",
		ws.Name,
		schedulingv1alpha1.Resource("locationdomains").WithVersion("v1alpha1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		a       admission.Attributes
		wantErr bool
	}{
		{
			name: "accepts no-op updates",
			a: updateAttr(&schedulingv1alpha1.LocationDomain{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: schedulingv1alpha1.LocationDomainSpec{
					Type: "Workloads",
					Instances: schedulingv1alpha1.InstancesReference{
						Resource: schedulingv1alpha1.GroupVersionResource{Group: "workload.kcp.dev", Version: "v1alpha1", Resource: "workloadclusters"},
					},
				},
			},
				&schedulingv1alpha1.LocationDomain{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: schedulingv1alpha1.LocationDomainSpec{
						Type: "Workloads",
						Instances: schedulingv1alpha1.InstancesReference{
							Resource: schedulingv1alpha1.GroupVersionResource{Group: "workload.kcp.dev", Version: "v1alpha1", Resource: "workloadclusters"},
						},
					},
				}),
		},
		{
			name: "accepts create",
			a: createAttr(&schedulingv1alpha1.LocationDomain{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: schedulingv1alpha1.LocationDomainSpec{
					Type: "Workloads",
					Instances: schedulingv1alpha1.InstancesReference{
						Resource: schedulingv1alpha1.GroupVersionResource{Group: "workload.kcp.dev", Version: "v1alpha1", Resource: "workloadclusters"},
					},
				},
			}),
		},
		{
			name: "accepts types other than Workloads",
			a: createAttr(&schedulingv1alpha1.LocationDomain{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: schedulingv1alpha1.LocationDomainSpec{
					Type: "Foo",
					Instances: schedulingv1alpha1.InstancesReference{
						Resource: schedulingv1alpha1.GroupVersionResource{Group: "workload.kcp.dev", Version: "v1alpha1", Resource: "workloadclusters"},
					},
				},
			}),
			wantErr: true,
		},
		{
			name: "rejects type mutations",
			a: updateAttr(&schedulingv1alpha1.LocationDomain{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: schedulingv1alpha1.LocationDomainSpec{
					Type: "Workloads",
					Instances: schedulingv1alpha1.InstancesReference{
						Resource: schedulingv1alpha1.GroupVersionResource{Group: "workload.kcp.dev", Version: "v1alpha1", Resource: "workloadclusters"},
					},
				},
			},
				&schedulingv1alpha1.LocationDomain{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: schedulingv1alpha1.LocationDomainSpec{
						Type: "Universal",
					},
				}),
			wantErr: true,
		},
		{
			name: "rejects everything but workloadclusters",
			a: createAttr(&schedulingv1alpha1.LocationDomain{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: schedulingv1alpha1.LocationDomainSpec{
					Type: "Workloads",
					Instances: schedulingv1alpha1.InstancesReference{
						Resource: schedulingv1alpha1.GroupVersionResource{
							Version:  "v1",
							Resource: "foo",
						},
					},
				},
			}),
			wantErr: true,
		},
		{
			name: "rejects instances workspace mutations",
			a: updateAttr(&schedulingv1alpha1.LocationDomain{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
				Spec: schedulingv1alpha1.LocationDomainSpec{
					Type: "Workloads",
					Instances: schedulingv1alpha1.InstancesReference{
						Resource: schedulingv1alpha1.GroupVersionResource{Group: "workload.kcp.dev", Version: "v1alpha1", Resource: "workloadclusters"},
						Workspace: &schedulingv1alpha1.WorkspaceExportReference{
							WorkspaceName: "foo",
						},
					},
				},
			},
				&schedulingv1alpha1.LocationDomain{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
					},
					Spec: schedulingv1alpha1.LocationDomainSpec{
						Type: "Workloads",
						Instances: schedulingv1alpha1.InstancesReference{
							Resource: schedulingv1alpha1.GroupVersionResource{Group: "workload.kcp.dev", Version: "v1alpha1", Resource: "workloadclusters"},
							Workspace: &schedulingv1alpha1.WorkspaceExportReference{
								WorkspaceName: "bar",
							},
						},
					},
				}),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &clusterWorkspace{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root:org")})
			if err := o.Validate(ctx, tt.a, nil); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
