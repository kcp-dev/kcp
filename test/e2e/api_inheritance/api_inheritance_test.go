/*
Copyright 2021 The KCP Authors.

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

package api_inheritance

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubernetesclientset "k8s.io/client-go/kubernetes"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	fixturewildwest "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIInheritance(t *testing.T) {
	t.Parallel()

	const serverName = "main"

	type InheritFrom int
	const (
		fromRoot InheritFrom = iota
		fromOrg
	)
	testCases := []struct {
		name               string
		sourceInheritsFrom InheritFrom
	}{
		{
			name:               "transitively inherit from root workspace",
			sourceInheritsFrom: fromRoot,
		},
		{
			name:               "transitively inherit from org workspace",
			sourceInheritsFrom: fromOrg,
		},
	}

	server := framework.SharedKcpServer(t)

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				withDeadline, cancel := context.WithDeadline(ctx, deadline)
				t.Cleanup(cancel)
				ctx = withDeadline
			}

			cfg, err := server.DefaultConfig()
			require.NoError(t, err)

			orgClusterName := framework.NewOrganizationFixture(t, server)

			// These are the cluster name paths (i.e. /clusters/$org:$workspace) for our two workspaces.
			_, org, err := helper.ParseLogicalClusterName(orgClusterName)
			require.NoError(t, err)
			var (
				sourceWorkspaceClusterName = org + ":source"
				targetWorkspaceClusterName = org + ":target"
			)

			apiExtensionsClients, err := apiextensionsclient.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct apiextensions client for server")
			kcpClients, err := clientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct kcp client for server")
			kubeClusterClient, err := kubernetesclientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct kube client for server")

			t.Logf("Creating \"source\" workspaces in org lcluster %s, inheriting sourceInheritsFrom %q", orgClusterName, orgClusterName)
			var sourceInheritsFrom string
			switch testCase.sourceInheritsFrom {
			case fromRoot:
				sourceInheritsFrom = helper.RootCluster
			case fromOrg:
				sourceInheritsFrom = orgClusterName
			}
			orgKcpClient := kcpClients.Cluster(orgClusterName)
			sourceWorkspace := &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "source",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					InheritFrom: sourceInheritsFrom,
				},
			}
			_, err = orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, sourceWorkspace, metav1.CreateOptions{})
			require.NoError(t, err, "error creating source workspace")
			sourceKubeClient := kubeClusterClient.Cluster(sourceWorkspaceClusterName)
			_, err = sourceKubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "default",
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err, "error creating defaul tnamespace")

			server.Artifact(t, func() (runtime.Object, error) {
				return orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, "source", metav1.GetOptions{})
			})

			t.Logf("Creating \"target\" workspace in org lcluster %s, not inheriting sourceInheritsFrom any workspace", orgClusterName)
			targetWorkspace := &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "target",
				},
			}
			targetWorkspace, err = orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, targetWorkspace, metav1.CreateOptions{})
			require.NoError(t, err, "error creating target workspace")
			require.NoError(t, err, "error creating source workspace")
			targetKubeClient := kubeClusterClient.Cluster(targetWorkspaceClusterName)
			_, err = targetKubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "default",
				},
			}, metav1.CreateOptions{})
			require.NoError(t, err, "error creating default namespace")

			server.Artifact(t, func() (runtime.Object, error) {
				return orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, "target", metav1.GetOptions{})
			})

			t.Logf("Install a cowboys CRD into \"source\" workspace")
			sourceCrdClient := apiExtensionsClients.Cluster(sourceWorkspaceClusterName).ApiextensionsV1().CustomResourceDefinitions()
			fixturewildwest.Create(t, sourceCrdClient, metav1.GroupResource{Group: wildwest.GroupName, Resource: "cowboys"})

			expectGroupInDiscovery := func(lcluster, group string) error {
				if err := wait.PollImmediateUntilWithContext(ctx, 100*time.Millisecond, func(c context.Context) (done bool, err error) {
					groups, err := kcpClients.Cluster(lcluster).Discovery().ServerGroups()
					if err != nil {
						return false, fmt.Errorf("error retrieving source workspace group discovery: %w", err)
					}
					if groupExists(groups, group) {
						return true, nil
					}
					return false, nil
				}); err != nil {
					return fmt.Errorf("source workspace discovery is missing group %q", group)
				}
				return nil
			}

			t.Logf("Make sure %q API group shows up in \"source\" workspace group discovery, inherited from %s", tenancy.GroupName, sourceInheritsFrom)
			err = expectGroupInDiscovery(sourceWorkspaceClusterName, tenancy.GroupName)
			require.NoError(t, err)

			t.Logf("Make sure \"cowboys\" API resource shows up in \"source\" workspace group version discovery")
			resources, err := kcpClients.Cluster(sourceWorkspaceClusterName).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
			require.NoError(t, err, "error retrieving source workspace cluster API discovery")
			require.True(t, resourceExists(resources, "cowboys"), "source workspace discovery is missing clusters resource")

			t.Logf("Creating cowboy CR in \"source\" workspace, and later make sure CRs are not inherited by the \"target\" workspace")
			sourceWorkspaceCowboy := &wildwestv1alpha1.Cowboy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "source-cowboy",
					Namespace: "default",
				},
			}
			wildwestClusterClient, err := wildwestclientset.NewClusterForConfig(cfg)
			require.NoError(t, err)
			sourceCowboyClient := wildwestClusterClient.Cluster(sourceWorkspaceClusterName).WildwestV1alpha1().Cowboys("default")
			_, err = sourceCowboyClient.Create(ctx, sourceWorkspaceCowboy, metav1.CreateOptions{})
			require.NoError(t, err, "Error creating sourceWorkspaceCowboy inside source")

			t.Logf("Make sure %s API group does NOT show up yet in \"target\" workspace group discovery", wildwest.GroupName)
			groups, err := kcpClients.Cluster(targetWorkspaceClusterName).Discovery().ServerGroups()
			require.NoError(t, err, "error retrieving target workspace group discovery")
			require.False(t, groupExists(groups, wildwest.GroupName),
				"should not have seen %s API group in target workspace group discovery", wildwest.GroupName)

			t.Logf("Update \"target\" workspace to inherit sourceInheritsFrom \"source\" workspace")
			targetWorkspace, err = orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, targetWorkspace.GetName(), metav1.GetOptions{})
			require.NoError(t, err, "error retrieving target workspace")

			targetWorkspace.Spec.InheritFrom = "source"
			if _, err = orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Update(ctx, targetWorkspace, metav1.UpdateOptions{}); err != nil {
				t.Errorf("error updating target workspace to inherit sourceInheritsFrom source: %v", err)
				return
			}

			t.Logf("Make sure API group sourceInheritsFrom inheritance shows up in target workspace group discovery")
			err = expectGroupInDiscovery(targetWorkspaceClusterName, wildwest.GroupName)
			require.NoError(t, err)

			t.Logf("Make sure \"cowboys\" resource inherited sourceInheritsFrom \"source\" shows up in \"target\" workspace group version discovery")
			resources, err = kcpClients.Cluster(targetWorkspaceClusterName).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
			require.NoError(t, err, "error retrieving target wildwest API discovery")
			require.True(t, resourceExists(resources, "cowboys"), "target workspace discovery is missing cowboys")

			t.Logf("Make sure we can perform CRUD operations in the \"target\" cluster for the inherited API")

			t.Logf("Make sure list shows nothing to start")
			targetCowboyClient := wildwestClusterClient.Cluster(targetWorkspaceClusterName).WildwestV1alpha1().Cowboys("default")
			cowboys, err := targetCowboyClient.List(ctx, metav1.ListOptions{})
			require.NoError(t, err, "error listing clusters inside target")
			require.Zero(t, len(cowboys.Items), "expected 0 clusters inside target")

			t.Logf("Create a cluster CR in the \"target\" workspace")
			targetWorkspaceCowboy := &wildwestv1alpha1.Cowboy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "target-cowboy",
					Namespace: "default",
				},
			}
			_, err = targetCowboyClient.Create(ctx, targetWorkspaceCowboy, metav1.CreateOptions{})
			require.NoError(t, err, "error creating targetWorkspaceCowboy inside target")

			t.Logf("Make sure source has \"source-cowboy\" and target have \"target-cowboy\" cluster CR")
			cowboys, err = sourceCowboyClient.List(ctx, metav1.ListOptions{})
			require.NoError(t, err, "error listing cowboys inside source")
			require.Equal(t, 1, len(cowboys.Items), "expected 1 cowboys inside source")
			require.Equal(t, "source-cowboy", cowboys.Items[0].Name, "unexpected name for source cowboy")

			cowboys, err = targetCowboyClient.List(ctx, metav1.ListOptions{})
			require.NoError(t, err, "error listing clusters inside target")
			require.Equal(t, 1, len(cowboys.Items), "expected 1 cowboys inside target")
			require.Equal(t, "target-cowboy", cowboys.Items[0].Name, "unexpected name for target cowboy")
		})
	}
}

func groupExists(list *metav1.APIGroupList, group string) bool {
	for _, g := range list.Groups {
		if g.Name == group {
			return true
		}
	}
	return false
}

func resourceExists(list *metav1.APIResourceList, resource string) bool {
	for _, r := range list.APIResources {
		if r.Name == resource {
			return true
		}
	}
	return false
}
