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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestSchedulingAPIBinding(t *testing.T) {
	t.Parallel()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	source := framework.SharedKcpServer(t)

	orgClusterName := framework.NewOrganizationFixture(t, source)
	negotiationClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName, "Universal")
	userClusterName := framework.NewWorkspaceFixture(t, source, orgClusterName, "Universal")

	kubeClusterClient, err := kubernetes.NewClusterForConfig(source.DefaultConfig(t))
	require.NoError(t, err)
	kcpClusterClient, err := kcpclient.NewClusterForConfig(source.DefaultConfig(t))
	require.NoError(t, err)

	t.Logf("Check that there is no services resource in the ser workspace")
	_, err = kubeClusterClient.Cluster(userClusterName).CoreV1().Services("").List(ctx, metav1.ListOptions{})
	require.Error(t, err)

	t.Logf("Creating a WorkloadCluster and syncer in %s", negotiationClusterName)
	_ = framework.SyncerFixture{
		ResourcesToSync:      sets.NewString("services"),
		UpstreamServer:       source,
		WorkspaceClusterName: negotiationClusterName,
		InstallCRDs: func(config *rest.Config, isLogicalCluster bool) {
			if !isLogicalCluster {
				// Only need to install services and ingresses in a logical cluster
				return
			}
			sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
			require.NoError(t, err, "failed to create apiextensions client")
			t.Logf("Installing test CRDs into sink cluster...")
			kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
				metav1.GroupResource{Group: "core.k8s.io", Resource: "services"},
			)
			require.NoError(t, err)
		},
	}.Start(t)

	t.Logf("Wait for APIResourceImports to show up in the negotiation workspace")
	require.Eventually(t, func() bool {
		imports, err := kcpClusterClient.Cluster(negotiationClusterName).ApiresourceV1alpha1().APIResourceImports().List(ctx, metav1.ListOptions{})
		if err != nil {
			klog.Errorf("Failed to list APIResourceImports: %v", err)
			return false
		}

		return len(imports.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for NegotiatedAPIResources to show up in the negotiation workspace")
	require.Eventually(t, func() bool {
		resources, err := kcpClusterClient.Cluster(negotiationClusterName).ApiresourceV1alpha1().NegotiatedAPIResources().List(ctx, metav1.ListOptions{})
		if err != nil {
			klog.Errorf("Failed to list NegotiatedAPIResources: %v", err)
			return false
		}

		return len(resources.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Create an APIExport in the negotiation domain")
	export := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "workloads",
		},
		Spec: apisv1alpha1.APIExportSpec{},
	}
	_, err = kcpClusterClient.Cluster(negotiationClusterName).ApisV1alpha1().APIExports().Create(ctx, export, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Wait for APIResourceSchemas to show up in the negotiation workspace")
	require.Eventually(t, func() bool {
		schemas, err := kcpClusterClient.Cluster(negotiationClusterName).ApisV1alpha1().APIResourceSchemas().List(ctx, metav1.ListOptions{})
		if err != nil {
			klog.Errorf("Failed to list APIResourceSchemas: %v", err)
			return false
		}

		return len(schemas.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Wait for APIResourceSchemas to show up in the APIExport spec")
	require.Eventually(t, func() bool {
		export, err := kcpClusterClient.Cluster(negotiationClusterName).ApisV1alpha1().APIExports().Get(ctx, "workloads", metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Failed to get APIExport: %v", err)
			return false
		}

		return len(export.Spec.LatestResourceSchemas) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Create a location domain")
	domain := &schedulingv1alpha1.LocationDomain{
		ObjectMeta: metav1.ObjectMeta{
			Name: "standard-compute",
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
			WorkspaceSelector: &schedulingv1alpha1.WorkspaceSelector{
				Types:    []schedulingv1alpha1.WorkspaceType{"Universal"},
				Priority: 1,
			},
		},
	}
	_, err = kcpClusterClient.Cluster(orgClusterName).SchedulingV1alpha1().LocationDomains().Create(ctx, domain, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for the Location to show in the org")
	require.Eventually(t, func() bool {
		locations, err := kcpClusterClient.Cluster(orgClusterName).SchedulingV1alpha1().Locations().List(ctx, metav1.ListOptions{})
		if err != nil {
			klog.Errorf("Failed to list Locations: %v", err)
			return false
		}

		return len(locations.Items) > 0
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for the placement label on the user workspace")
	require.Eventually(t, func() bool {
		workspace, err := kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, userClusterName.Base(), metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Failed to get Workspace: %v", err)
			return false
		}

		bs, _ := yaml.Marshal(workspace)
		klog.Infof("Workspace:\n%s", string(bs))

		labelKey := schedulingv1alpha1.LocationDomainAssignmentLabelKeyForType("Workloads")
		return workspace.Labels[labelKey] != ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for the APIBinding to be created in the user workspace")
	require.Eventually(t, func() bool {
		if _, err := kcpClusterClient.Cluster(userClusterName).ApisV1alpha1().APIBindings().Get(ctx, "workloads", metav1.GetOptions{}); errors.IsNotFound(err) {
			return false
		} else if err != nil {
			klog.Errorf("Failed to list APIBindings: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Wait for being able to list Services in the user workspace")
	require.Eventually(t, func() bool {
		_, err := kubeClusterClient.Cluster(userClusterName).CoreV1().Services("").List(ctx, metav1.ListOptions{})
		if errors.IsNotFound(err) {
			return false
		} else if err != nil {
			klog.Errorf("Failed to list Services: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}
