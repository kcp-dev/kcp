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

package conformance

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/pkg/genericcontrolplane/clientutils"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestCrossLogicalClusterList(t *testing.T) {
	t.Parallel()

	const serverName = "main"

	f := framework.NewKcpFixture(t,
		framework.KcpConfig{
			Name: "main",
			Args: []string{
				"--run-controllers=false",
				"--unsupported-run-individual-controllers=workspace-scheduler",
			},
		},
	)

	// TODO(marun) Collapse this sub test into its parent. It's only
	// left in this form to simplify review of the transition to the
	// new fixture.
	t.Run("Ensure cross logical cluster list works", func(t *testing.T) {
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

		// Until we get rid of the multiClusterClientConfigRoundTripper and replace it with scoping,
		// make sure we don't break cross-logical cluster client listing.
		clientutils.EnableMultiCluster(cfg, nil, true)

		logicalClusters := []string{"root:one", "root:two", "root:three"}
		for i, logicalCluster := range logicalClusters {
			t.Logf("Bootstrapping ClusterWorkspace CRDs in logical cluster %s", logicalCluster)
			apiExtensionsClients, err := apiextensionsclient.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct apiextensions client for server")
			crdClient := apiExtensionsClients.Cluster(logicalCluster).ApiextensionsV1().CustomResourceDefinitions()
			workspaceCRDs := []metav1.GroupResource{
				{Group: tenancy.GroupName, Resource: "clusterworkspaces"},
			}
			err = configcrds.Create(ctx, crdClient, workspaceCRDs...)
			require.NoError(t, err, "failed to bootstrap CRDs")
			kcpClients, err := clientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct kcp client for server")

			t.Logf("Creating ClusterWorkspace CRs in logical cluster %s", logicalCluster)
			kcpClient := kcpClients.Cluster(logicalCluster)
			sourceWorkspace := &tenancyapi.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("ws-%d", i),
				},
			}
			_, err = kcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, sourceWorkspace, metav1.CreateOptions{})
			require.NoError(t, err, "error creating source workspace")

			server.Artifact(t, func() (runtime.Object, error) {
				return kcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, sourceWorkspace.Name, metav1.GetOptions{})
			})
		}

		t.Logf("Listing ClusterWorkspace CRs across logical clusters")
		kcpClients, err := clientset.NewClusterForConfig(cfg)
		require.NoError(t, err, "failed to construct kcp client for server")
		kcpClient := kcpClients.Cluster("*")
		workspaces, err := kcpClient.TenancyV1alpha1().ClusterWorkspaces().List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "error listing workspaces")

		t.Logf("Expecting at least those ClusterWorkspaces we created above")
		require.True(t, len(workspaces.Items) >= 3, "expected at least 3 workspaces")
		got := sets.NewString()
		for _, ws := range workspaces.Items {
			got.Insert(ws.ClusterName + "/" + ws.Name)
		}
		expected := sets.NewString("root:one/ws-0", "root:two/ws-1", "root:three/ws-2")
		require.True(t, got.IsSuperset(expected), "unexpected workspaces detected")
	})
}
