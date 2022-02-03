//go:build e2e
// +build e2e

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

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/pkg/genericcontrolplane/clientutils"

	"github.com/kcp-dev/kcp/config"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestCrossLogicalClusterList(t *testing.T) {
	const serverName = "main"

	framework.Run(t, "Ensure cross logical cluster list works", func(t framework.TestingTInterface, servers map[string]framework.RunningServer, artifactDir, dataDir string) {
		ctx := context.Background()
		if deadline, ok := t.Deadline(); ok {
			withDeadline, cancel := context.WithDeadline(ctx, deadline)
			t.Cleanup(cancel)
			ctx = withDeadline
		}
		if len(servers) != 1 {
			t.Errorf("incorrect number of servers: %d", len(servers))
			return
		}
		server := servers[serverName]
		cfg, err := server.Config()
		if err != nil {
			t.Error(err)
			return
		}

		// Until we get rid of the multiClusterClientConfigRoundTripper and replace it with scoping,
		// make sure we don't break cross-logical cluster client listing.
		clientutils.EnableMultiCluster(cfg, nil, true)

		logicalClusters := []string{"admin_one", "admin_two", "admin_three"}
		for i, logicalCluster := range logicalClusters {
			apiExtensionsClients, err := apiextensionsclient.NewClusterForConfig(cfg)
			if err != nil {
				t.Errorf("failed to construct apiextensions client for server: %v", err)
				return
			}

			crdClient := apiExtensionsClients.Cluster(logicalCluster).ApiextensionsV1().CustomResourceDefinitions()

			workspaceCRDs := []metav1.GroupKind{
				{Group: tenancy.GroupName, Kind: "workspaces"},
			}

			if err := config.BootstrapCustomResourceDefinitions(ctx, crdClient, workspaceCRDs); err != nil {
				t.Errorf("failed to bootstrap CRDs: %v", err)
				return
			}

			kcpClients, err := clientset.NewClusterForConfig(cfg)
			if err != nil {
				t.Errorf("failed to construct kcp client for server: %v", err)
				return
			}

			kcpClient := kcpClients.Cluster(logicalCluster)

			sourceWorkspace := &tenancyapi.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("ws-%d", i),
				},
			}
			_, err = kcpClient.TenancyV1alpha1().Workspaces().Create(ctx, sourceWorkspace, metav1.CreateOptions{})
			if err != nil {
				t.Errorf("error creating source workspace: %v", err)
				return
			}
			server.Artifact(t, func() (runtime.Object, error) {
				return kcpClient.TenancyV1alpha1().Workspaces().Get(ctx, sourceWorkspace.Name, metav1.GetOptions{})
			})
		}

		kcpClients, err := clientset.NewClusterForConfig(cfg)
		if err != nil {
			t.Errorf("failed to construct kcp client for server: %v", err)
			return
		}

		kcpClient := kcpClients.Cluster("*")
		workspaces, err := kcpClient.TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Errorf("error listing workspaces: %v", err)
			return
		}
		if len(workspaces.Items) != 3 {
			t.Errorf("expected 3 workspaces, got %d", len(workspaces.Items))
			return
		}
		got := sets.NewString()
		for _, ws := range workspaces.Items {
			got.Insert(ws.ClusterName + "/" + ws.Name)
		}
		expected := sets.NewString("admin_one/ws-0", "admin_two/ws-1", "admin_three/ws-2")
		if !expected.Equal(got) {
			t.Errorf("expected %v, got %v", expected, got)
		}

	}, framework.KcpConfig{
		Name: "main",
		Args: []string{"--install-workspace-scheduler"},
	})
}
