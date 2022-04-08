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

package cluster

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestSchedulingClusterWorkspaceController(t *testing.T) {
	t.Parallel()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgClusterName := framework.NewOrganizationFixture(t, source)
	negotiationClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName, "Universal")

	kcpClusterClient, err := kcpclient.NewClusterForConfig(source.DefaultConfig(t))
	require.NoError(t, err)

	t.Log("Create three location domains")
	newDomain := func(name string, selector *schedulingv1alpha1.WorkspaceSelector) *schedulingv1alpha1.LocationDomain {
		return &schedulingv1alpha1.LocationDomain{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Spec: schedulingv1alpha1.LocationDomainSpec{
				Type: "Workloads",
				Instances: schedulingv1alpha1.InstancesReference{
					Resource: schedulingv1alpha1.GroupVersionResource{
						Group:    "workload.kcp.dev",
						Version:  "v1alpha1",
						Resource: "workloadclusters",
					},
					Workspace: &schedulingv1alpha1.WorkspaceExportReference{
						WorkspaceName: schedulingv1alpha1.WorkspaceName(negotiationClusterName.Base()),
					},
				},
				Locations: []schedulingv1alpha1.LocationDomainLocationDefinition{
					{
						Name: "foo",
						Labels: map[schedulingv1alpha1.LabelKey]schedulingv1alpha1.LabelValue{
							"foo": "42",
						},
					},
				},
				WorkspaceSelector: selector,
			},
		}
	}
	for _, i := range []int32{1, 2, 3} {
		_, err = kcpClusterClient.Cluster(orgClusterName).SchedulingV1alpha1().LocationDomains().Create(ctx, newDomain(fmt.Sprintf("compute-%d", i), &schedulingv1alpha1.WorkspaceSelector{
			Types:    []schedulingv1alpha1.WorkspaceType{"Universal"},
			Priority: i - 2,
		}), metav1.CreateOptions{})
		require.NoError(t, err)
	}

	t.Logf("Create a workspace and wait for assignment")
	cluster3LabelValue := strings.ReplaceAll(orgClusterName.Join("compute-3").String(), ":", ".")
	workspaceClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName, "Universal")
	labelKey := schedulingv1alpha1.LocationDomainAssignmentLabelKeyForType("Workloads")
	require.Eventually(t, func() bool {
		workspace, err := kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspaceClusterName.Base(), metav1.GetOptions{})
		require.NoError(t, err)

		bs, _ := yaml.Marshal(workspace)
		klog.Infof(string(bs))

		assignedTo := workspace.Labels[labelKey]
		return assignedTo == cluster3LabelValue
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Remove assignment and wait for workspace to be reassigned")
	_, err = kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Patch(ctx, workspaceClusterName.Base(),
		types.MergePatchType, []byte(fmt.Sprintf(`{"metadata":{"labels":{%q:null}}}`, labelKey)), metav1.PatchOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		workspace, err := kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspaceClusterName.Base(), metav1.GetOptions{})
		require.NoError(t, err)

		assignedTo := workspace.Labels[labelKey]
		return assignedTo == cluster3LabelValue
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}
