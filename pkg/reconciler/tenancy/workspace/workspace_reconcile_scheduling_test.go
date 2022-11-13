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

package workspace

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsapi "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

func TestSchedulingReconciler(t *testing.T) {
	tests := []struct {
		name       string
		workspace  *tenancyv1alpha1.ClusterWorkspace
		shards     []*tenancyv1alpha1.ClusterWorkspaceShard
		want       *tenancyv1alpha1.ClusterWorkspace
		wantStatus reconcileStatus
		wantErr    bool
	}{
		// TODO(sttts): add test coverage for all old cases

		{
			name:      "no shards, not scheduled",
			workspace: phase(tenancyv1alpha1.WorkspacePhaseScheduling, workspace()),
			want: withConditions(phase(tenancyv1alpha1.WorkspacePhaseScheduling, workspace()),
				conditionsapi.Condition{
					Type:     tenancyv1alpha1.WorkspaceScheduled,
					Severity: conditionsapi.ConditionSeverityError,
					Status:   corev1.ConditionFalse,
					Reason:   tenancyv1alpha1.WorkspaceReasonUnschedulable,
				},
			),
			wantStatus: reconcileStatusContinue,
		},
		// TODO:(p0lyn0mial): fix me
		/*{
			name: "no shards, to be unscheduled",
			workspace: phase(tenancyv1alpha1.WorkspacePhaseScheduling,
				scheduled("root", "https://front-proxy/clusters/workspace", workspace())),
			want: withConditions(phase(tenancyv1alpha1.WorkspacePhaseScheduling, workspace()),
				conditionsapi.Condition{
					Type:     tenancyv1alpha1.WorkspaceScheduled,
					Severity: conditionsapi.ConditionSeverityError,
					Status:   corev1.ConditionFalse,
					Reason:   tenancyv1alpha1.WorkspaceReasonUnschedulable,
				},
			),
			wantStatus: reconcileStatusContinue,
		}
		{
			name: "no shards, already initializing",
			workspace: phase(tenancyv1alpha1.WorkspacePhaseInitializing,
				scheduled("root", "https://front-proxy/clusters/workspace", workspace())),
			want: withConditions(phase(tenancyv1alpha1.WorkspacePhaseInitializing,
				scheduled("root", "https://front-proxy/clusters/workspace", workspace())),
				conditionsapi.Condition{
					Type:   tenancyv1alpha1.WorkspaceScheduled,
					Status: corev1.ConditionTrue,
				},
				conditionsapi.Condition{
					Type:     tenancyv1alpha1.WorkspaceShardValid,
					Severity: conditionsapi.ConditionSeverityError,
					Status:   corev1.ConditionFalse,
					Reason:   tenancyv1alpha1.WorkspaceShardValidReasonShardNotFound,
				},
			),
			wantStatus: reconcileStatusContinue,
		},
		{
			name: "no shards, already ready",
			workspace: phase(tenancyv1alpha1.WorkspacePhaseReady,
				scheduled("root", "https://front-proxy/clusters/workspace", workspace())),
			want: withConditions(phase(tenancyv1alpha1.WorkspacePhaseReady,
				scheduled("root", "https://front-proxy/clusters/workspace", workspace())),
				conditionsapi.Condition{
					Type:   tenancyv1alpha1.WorkspaceScheduled,
					Status: corev1.ConditionTrue,
				},
				conditionsapi.Condition{
					Type:     tenancyv1alpha1.WorkspaceShardValid,
					Severity: conditionsapi.ConditionSeverityError,
					Status:   corev1.ConditionFalse,
					Reason:   tenancyv1alpha1.WorkspaceShardValidReasonShardNotFound,
				},
			),
			wantStatus: reconcileStatusContinue,
		},
		// TODO:(p0lyn0mial): fix me
		/*{
			name:      "happy case scheduling",
			workspace: phase(tenancyv1alpha1.WorkspacePhaseScheduling, workspace()),
			shards: []*tenancyv1alpha1.ClusterWorkspaceShard{
				withURLs("https://root", "https://front-proxy", shard("root")),
			},
			want: withConditions(phase(tenancyv1alpha1.WorkspacePhaseScheduling,
				scheduled("root", "https://front-proxy/clusters/workspace", workspace())),
				conditionsapi.Condition{
					Type:   tenancyv1alpha1.WorkspaceScheduled,
					Status: corev1.ConditionTrue,
				},
			),
			wantStatus: reconcileStatusContinue,
		},
		{
			name: "happy case rescheduling",
			workspace: phase(tenancyv1alpha1.WorkspacePhaseScheduling,
				scheduled("foo", "https://foo", workspace())),
			shards: []*tenancyv1alpha1.ClusterWorkspaceShard{
				withURLs("https://root", "https://front-proxy", shard("root")),
			},
			want: withConditions(phase(tenancyv1alpha1.WorkspacePhaseScheduling,
				scheduled("root", "https://front-proxy/clusters/workspace", workspace())),
				conditionsapi.Condition{
					Type:   tenancyv1alpha1.WorkspaceScheduled,
					Status: corev1.ConditionTrue,
				},
			),
			wantStatus: reconcileStatusContinue,
		},
		{
			name: "spec shard name",
			workspace: phase(tenancyv1alpha1.WorkspacePhaseScheduling,
				constrained(tenancyv1alpha1.ShardConstraints{Name: "foo"}, workspace())),
			shards: []*tenancyv1alpha1.ClusterWorkspaceShard{
				withLabels(map[string]string{"b": "2"}, withURLs("https://root", "https://front-proxy", shard("root"))),
				withLabels(map[string]string{"a": "1"}, withURLs("https://foo", "https://front-proxy", shard("foo"))),
			},
			want: withConditions(phase(tenancyv1alpha1.WorkspacePhaseScheduling,
				scheduled("foo", "https://front-proxy/clusters/workspace",
					constrained(tenancyv1alpha1.ShardConstraints{Name: "foo"}, workspace()))),
				conditionsapi.Condition{
					Type:   tenancyv1alpha1.WorkspaceScheduled,
					Status: corev1.ConditionTrue,
				},
			),
			wantStatus: reconcileStatusContinue,
		{
			name: "invalid spec shard name",
			workspace: phase(tenancyv1alpha1.WorkspacePhaseScheduling,
				constrained(tenancyv1alpha1.ShardConstraints{Name: "bar"}, workspace())),
			shards: []*tenancyv1alpha1.ClusterWorkspaceShard{
				withLabels(map[string]string{"b": "2"}, withURLs("https://root", "https://front-proxy", shard("root"))),
				withLabels(map[string]string{"a": "1"}, withURLs("https://foo", "https://front-proxy", shard("foo"))),
			},
			want: withConditions(phase(tenancyv1alpha1.WorkspacePhaseScheduling,
				constrained(tenancyv1alpha1.ShardConstraints{Name: "bar"}, workspace())),
				conditionsapi.Condition{
					Type:     tenancyv1alpha1.WorkspaceScheduled,
					Severity: conditionsapi.ConditionSeverityError,
					Status:   corev1.ConditionFalse,
					Reason:   tenancyv1alpha1.WorkspaceReasonUnschedulable,
				},
			),
			wantStatus: reconcileStatusContinue,
		},
		/*{
			name: "spec shard selector",
			workspace: phase(tenancyv1alpha1.WorkspacePhaseScheduling,
				constrained(tenancyv1alpha1.ShardConstraints{Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"a": "1"}},
				}, workspace())),
			shards: []*tenancyv1alpha1.ClusterWorkspaceShard{
				withLabels(map[string]string{"b": "2"}, withURLs("https://root", "https://front-proxy", shard("root"))),
				withLabels(map[string]string{"a": "1"}, withURLs("https://foo", "https://front-proxy", shard("foo"))),
			},
			want: withConditions(phase(tenancyv1alpha1.WorkspacePhaseScheduling,
				scheduled("foo", "https://front-proxy/clusters/workspace",
					constrained(tenancyv1alpha1.ShardConstraints{Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"a": "1"}},
					}, workspace()))),
				conditionsapi.Condition{
					Type:   tenancyv1alpha1.WorkspaceScheduled,
					Status: corev1.ConditionTrue,
				},
			),
			wantStatus: reconcileStatusContinue,
		},*/
		{
			name: "invalid spec shard selector",
			workspace: phase(tenancyv1alpha1.WorkspacePhaseScheduling,
				constrained(tenancyv1alpha1.ShardConstraints{Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"c": "1"}},
				}, workspace())),
			shards: []*tenancyv1alpha1.ClusterWorkspaceShard{
				withLabels(map[string]string{"b": "2"}, withURLs("https://root", "https://front-proxy", shard("root"))),
				withLabels(map[string]string{"a": "1"}, withURLs("https://foo", "https://front-proxy", shard("foo"))),
			},
			want: withConditions(phase(tenancyv1alpha1.WorkspacePhaseScheduling,
				constrained(tenancyv1alpha1.ShardConstraints{Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"c": "1"}},
				}, workspace())),
				conditionsapi.Condition{
					Type:     tenancyv1alpha1.WorkspaceScheduled,
					Severity: conditionsapi.ConditionSeverityError,
					Status:   corev1.ConditionFalse,
					Reason:   tenancyv1alpha1.WorkspaceReasonUnschedulable,
				},
			),
			wantStatus: reconcileStatusContinue,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// TODO(p0lyn0mial): write new tests for scheduling phase
			//t.Skip()
			r := &schedulingReconciler{
				getShard: func(name string) (*tenancyv1alpha1.ClusterWorkspaceShard, error) {
					for _, shard := range tt.shards {
						if shard.Name == name {
							return shard, nil
						}
					}
					return nil, errors.NewNotFound(tenancyv1alpha1.Resource("clusterworkspaceshard"), name)
				},
				listShards: func(selector labels.Selector) ([]*tenancyv1alpha1.ClusterWorkspaceShard, error) {
					var shards []*tenancyv1alpha1.ClusterWorkspaceShard
					for _, shard := range tt.shards {
						if selector.Matches(labels.Set(shard.Labels)) {
							shards = append(shards, shard)
						}
					}
					return shards, nil
				},
			}
			ws := tt.workspace.DeepCopy()
			status, err := r.reconcile(context.Background(), ws)
			if (err != nil) != tt.wantErr {
				t.Errorf("unexpected error: error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if status != tt.wantStatus {
				t.Errorf("unexpected status: got = %v, want %v", status, tt.want)
			}

			// prune conditions for easier comparison
			for i := range ws.Status.Conditions {
				ws.Status.Conditions[i].LastTransitionTime = metav1.Time{}
				ws.Status.Conditions[i].Message = ""
			}

			if diff := cmp.Diff(tt.want, ws); diff != "" {
				t.Errorf("unexpected workspace (-want +got):\n%s", diff)
			}
		})
	}
}

func workspace() *tenancyv1alpha1.ClusterWorkspace {
	return &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "workspace",
		},
	}
}

func phase(phase tenancyv1alpha1.WorkspacePhaseType, shard *tenancyv1alpha1.ClusterWorkspace) *tenancyv1alpha1.ClusterWorkspace {
	shard.Status.Phase = phase
	return shard
}

func scheduled(shard string, baseURL string, ws *tenancyv1alpha1.ClusterWorkspace) *tenancyv1alpha1.ClusterWorkspace {
	ws.Status.Location.Current = shard
	ws.Status.BaseURL = baseURL
	return ws
}

func constrained(constraints tenancyv1alpha1.ShardConstraints, ws *tenancyv1alpha1.ClusterWorkspace) *tenancyv1alpha1.ClusterWorkspace {
	ws.Spec.Shard = &constraints
	return ws
}

func withConditions(ws *tenancyv1alpha1.ClusterWorkspace, conditions ...conditionsapi.Condition) *tenancyv1alpha1.ClusterWorkspace {
	ws.Status.Conditions = append(ws.Status.Conditions, conditions...)
	return ws
}

func shard(name string) *tenancyv1alpha1.ClusterWorkspaceShard {
	return &tenancyv1alpha1.ClusterWorkspaceShard{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

func withURLs(baseURL, externalURL string, shard *tenancyv1alpha1.ClusterWorkspaceShard) *tenancyv1alpha1.ClusterWorkspaceShard {
	shard.Spec.BaseURL = baseURL
	shard.Spec.ExternalURL = externalURL
	return shard
}

func withLabels(labels map[string]string, shard *tenancyv1alpha1.ClusterWorkspaceShard) *tenancyv1alpha1.ClusterWorkspaceShard {
	shard.Labels = labels
	return shard
}
