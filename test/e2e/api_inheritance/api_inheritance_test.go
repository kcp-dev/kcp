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

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	apiresourceapi "github.com/kcp-dev/kcp/pkg/apis/apiresource"
	"github.com/kcp-dev/kcp/pkg/apis/cluster"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIInheritance(t *testing.T) {
	t.Parallel()

	const serverName = "main"

	testCases := []struct {
		name string

		// the lcluster used to create source and target workspaces in
		orglogicalClusterName string
		orgPrefix             string
	}{
		{
			name:                  "transitively inherit from root workspace",
			orgPrefix:             helper.RootCluster,
			orglogicalClusterName: helper.RootCluster,
		},
		{
			name:                  "transitively inherit from some other org workspace",
			orgPrefix:             "myorg",
			orglogicalClusterName: "root_myorg",
		},
	}

	f := framework.NewKcpFixture(t,
		framework.KcpConfig{
			Name: serverName,
			Args: []string{
				"--run-controllers=false",
				"--unsupported-run-individual-controllers=workspace-scheduler",
			},
		},
	)

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

			require.Equal(t, 1, len(f.Servers), "incorrect number of servers")
			server := f.Servers[serverName]

			cfg, err := server.Config()
			require.NoError(t, err)

			apiExtensionsClients, err := apiextensionsclient.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct apiextensions client for server")

			t.Logf("Bootstrapping workspace CRD into org lcluster %s", testCase.orglogicalClusterName)
			orgCRDClient := apiExtensionsClients.Cluster(testCase.orglogicalClusterName).ApiextensionsV1().CustomResourceDefinitions()
			workspaceCRDs := []metav1.GroupResource{
				{Group: tenancy.GroupName, Resource: "clusterworkspaces"},
			}
			err = configcrds.Create(ctx, orgCRDClient, workspaceCRDs...)
			require.NoError(t, err, "failed to bootstrap CRDs")

			kcpClients, err := clientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct kcp client for server")

			t.Logf("Creating \"source\" workspaces in org lcluster %s, inheriting from %q", testCase.orglogicalClusterName, helper.RootCluster)
			orgKcpClient := kcpClients.Cluster(testCase.orglogicalClusterName)
			sourceWorkspace := &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "source",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
					InheritFrom: helper.RootCluster,
				},
			}
			_, err = orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, sourceWorkspace, metav1.CreateOptions{})
			require.NoError(t, err, "error creating source workspace")

			server.Artifact(t, func() (runtime.Object, error) {
				return orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, "source", metav1.GetOptions{})
			})

			t.Logf("Creating \"target\" workspace in org lcluster %s, not inheriting from any workspace", testCase.orglogicalClusterName)
			targetWorkspace := &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "target",
				},
			}
			targetWorkspace, err = orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, targetWorkspace, metav1.CreateOptions{})
			require.NoError(t, err, "error creating target workspace")

			server.Artifact(t, func() (runtime.Object, error) {
				return orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, "target", metav1.GetOptions{})
			})

			// These are the cluster name paths (i.e. /clusters/$org_$workspace) for our two workspaces.
			var (
				sourceWorkspaceClusterName = testCase.orgPrefix + "_source"
				targetWorkspaceClusterName = testCase.orgPrefix + "_target"
			)

			t.Logf("Install a clusters CRD into \"source\" workspace")
			crdsForWorkspaces := []metav1.GroupResource{
				{Group: cluster.GroupName, Resource: "clusters"},
			}
			sourceCrdClient := apiExtensionsClients.Cluster(sourceWorkspaceClusterName).ApiextensionsV1().CustomResourceDefinitions()

			err = configcrds.Create(ctx, sourceCrdClient, crdsForWorkspaces...)
			require.NoError(t, err, "failed to bootstrap CRDs in source")

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

			t.Logf("Make sure %q API group shows up in \"source\" workspace group discovery, inherited from root", apiresourceapi.GroupName)
			err = expectGroupInDiscovery(sourceWorkspaceClusterName, apiresourceapi.GroupName)
			require.NoError(t, err)

			t.Logf("Make sure %q API group shows up in \"source\" workspace group discovery, inherited from org", cluster.GroupName)
			err = expectGroupInDiscovery(sourceWorkspaceClusterName, cluster.GroupName)
			require.NoError(t, err)

			t.Logf("Make sure \"clusters\" API resource shows up in \"source\" workspace group version discovery")
			resources, err := kcpClients.Cluster(sourceWorkspaceClusterName).Discovery().ServerResourcesForGroupVersion(clusterv1alpha1.SchemeGroupVersion.String())
			require.NoError(t, err, "error retrieving source workspace cluster API discovery")
			require.True(t, resourceExists(resources, "clusters"), "source workspace discovery is missing clusters resource")

			t.Logf("Creating cluster CR in \"source\" workspace, and later make sure CRs are not inherited by the \"target\" workspace")
			sourceWorkspaceCluster := &clusterv1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "source-cluster",
				},
			}
			sourceClusterClient := kcpClients.Cluster(sourceWorkspaceClusterName).ClusterV1alpha1().Clusters()
			_, err = sourceClusterClient.Create(ctx, sourceWorkspaceCluster, metav1.CreateOptions{})
			require.NoError(t, err, "Error creating sourceWorkspaceCluster inside source")

			server.Artifact(t, func() (runtime.Object, error) {
				return sourceClusterClient.Get(ctx, "source-cluster", metav1.GetOptions{})
			})

			t.Logf("Make sure %s API group does NOT show up yet in \"target\" workspace group discovery", cluster.GroupName)
			groups, err := kcpClients.Cluster(targetWorkspaceClusterName).Discovery().ServerGroups()
			require.NoError(t, err, "error retrieving target workspace group discovery")
			require.False(t, groupExists(groups, cluster.GroupName),
				"should not have seen cluster API group in target workspace group discovery")

			t.Logf("Update \"target\" workspace to inherit from \"source\" workspace")
			targetWorkspace, err = orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, targetWorkspace.GetName(), metav1.GetOptions{})
			require.NoError(t, err, "error retrieving target workspace")

			targetWorkspace.Spec.InheritFrom = "source"
			if _, err = orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Update(ctx, targetWorkspace, metav1.UpdateOptions{}); err != nil {
				t.Errorf("error updating target workspace to inherit from source: %v", err)
				return
			}

			t.Logf("Make sure API group from inheritance shows up in target workspace group discovery")
			err = expectGroupInDiscovery(targetWorkspaceClusterName, cluster.GroupName)
			require.NoError(t, err)

			t.Logf("Make sure \"clusters\" resource inherited from \"source\" shows up in \"target\" workspace group version discovery")
			resources, err = kcpClients.Cluster(targetWorkspaceClusterName).Discovery().ServerResourcesForGroupVersion(clusterv1alpha1.SchemeGroupVersion.String())
			require.NoError(t, err, "error retrieving target workspace cluster API discovery")
			require.True(t, resourceExists(resources, "clusters"), "target workspace discovery is missing clusters resource")

			t.Logf("Make sure we can perform CRUD operations in the \"target\" cluster for the inherited API")

			t.Logf("Make sure list shows nothing to start")
			targetClusterClient := kcpClients.Cluster(targetWorkspaceClusterName).ClusterV1alpha1().Clusters()
			clusters, err := targetClusterClient.List(ctx, metav1.ListOptions{})
			require.NoError(t, err, "error listing clusters inside target")
			require.Zero(t, len(clusters.Items), "expected 0 clusters inside target")

			t.Logf("Create a cluster CR in the \"target\" workspace")
			targetWorkspaceCluster := &clusterv1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: "target-cluster",
				},
			}
			_, err = targetClusterClient.Create(ctx, targetWorkspaceCluster, metav1.CreateOptions{})
			require.NoError(t, err, "error creating targetWorkspaceCluster inside target")

			server.Artifact(t, func() (runtime.Object, error) {
				return targetClusterClient.Get(ctx, "target-cluster", metav1.GetOptions{})
			})

			t.Logf("Make sure source has \"source-cluster\" and target have \"target-cluster\" cluster CR")
			clusters, err = sourceClusterClient.List(ctx, metav1.ListOptions{})
			require.NoError(t, err, "error listing clusters inside source")
			require.Equal(t, 1, len(clusters.Items), "expected 1 cluster inside source")
			require.Equal(t, "source-cluster", clusters.Items[0].Name, "unexpected name for source cluster")

			clusters, err = targetClusterClient.List(ctx, metav1.ListOptions{})
			require.NoError(t, err, "error listing clusters inside target")
			require.Equal(t, 1, len(clusters.Items), "expected 1 cluster inside target")
			require.Equal(t, "target-cluster", clusters.Items[0].Name, "unexpected name for target cluster")

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
