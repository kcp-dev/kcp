//go:build e2e
// +build e2e

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

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/kcp/config"
	"github.com/kcp-dev/kcp/pkg/apis/cluster"
	clusterapi "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIInheritance(t *testing.T) {
	const serverName = "main"

	framework.Run(t, "Basic workspace API inheritance", func(t framework.TestingTInterface, servers map[string]framework.RunningServer, artifactDir, dataDir string) {
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

		apiExtensionsClients, err := apiextensionsclient.NewClusterForConfig(cfg)
		if err != nil {
			t.Errorf("failed to construct apiextensions client for server: %v", err)
			return
		}
		crdAdminClient := apiExtensionsClients.Cluster("admin").ApiextensionsV1().CustomResourceDefinitions()

		// Register CRDs in the admin logical cluster
		adminClusterCRDs := []metav1.GroupKind{
			{Group: tenancy.GroupName, Kind: "workspaces"},
		}

		if err := config.BootstrapCustomResourceDefinitions(ctx, crdAdminClient, adminClusterCRDs); err != nil {
			t.Errorf("failed to bootstrap CRDs: %v", err)
			return
		}

		kcpClients, err := clientset.NewClusterForConfig(cfg)
		if err != nil {
			t.Errorf("failed to construct kcp client for server: %v", err)
			return
		}

		// For now, API inheritance only supports workspaces in the admin logical cluster, so let's create
		// a couple there.
		kcpAdminClient := kcpClients.Cluster("admin")

		sourceWorkspace := &tenancyapi.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "source",
			},
		}
		_, err = kcpAdminClient.TenancyV1alpha1().Workspaces().Create(ctx, sourceWorkspace, metav1.CreateOptions{})
		if err != nil {
			t.Errorf("error creating source workspace: %v", err)
			return
		}
		server.Artifact(t, func() (runtime.Object, error) {
			return kcpAdminClient.TenancyV1alpha1().Workspaces().Get(ctx, "source", metav1.GetOptions{})
		})

		targetWorkspace := &tenancyapi.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "target",
			},
		}
		targetWorkspace, err = kcpAdminClient.TenancyV1alpha1().Workspaces().Create(ctx, targetWorkspace, metav1.CreateOptions{})
		if err != nil {
			t.Errorf("error creating target workspace: %v", err)
			return
		}
		server.Artifact(t, func() (runtime.Object, error) {
			return kcpAdminClient.TenancyV1alpha1().Workspaces().Get(ctx, "target", metav1.GetOptions{})
		})

		// Install a CRD into source workspace
		crdsForWorkspaces := []metav1.GroupKind{
			{Group: cluster.GroupName, Kind: "clusters"},
		}
		sourceCrdClient := apiExtensionsClients.Cluster("source").ApiextensionsV1().CustomResourceDefinitions()
		if err := config.BootstrapCustomResourceDefinitions(ctx, sourceCrdClient, crdsForWorkspaces); err != nil {
			t.Errorf("failed to bootstrap CRDs: %v", err)
			return
		}

		// Make sure API group from CRD shows up in source workspace group discovery
		if err := wait.PollImmediateUntilWithContext(ctx, 100*time.Millisecond, func(c context.Context) (done bool, err error) {
			groups, err := kcpClients.Cluster("source").Discovery().ServerGroups()
			if err != nil {
				return false, fmt.Errorf("error retrieving source workspace group discovery: %w", err)
			}
			if groupExists(groups, cluster.GroupName) {
				return true, nil
			}
			return false, nil
		}); err != nil {
			t.Errorf("source workspace discovery is missing group %q", cluster.GroupName)
			return
		}

		// Make sure API resource from CRD shows up in source workspace group version discovery
		resources, err := kcpClients.Cluster("source").Discovery().ServerResourcesForGroupVersion(clusterapi.SchemeGroupVersion.String())
		if err != nil {
			t.Errorf("error retrieving source workspace cluster API discovery: %v", err)
			return
		}
		if !resourceExists(resources, "clusters") {
			t.Errorf("source workspace discovery is missing clusters resource")
			return
		}

		// This cluster will be created in the source workspace. This is to ensure that it doesn't
		// leak into the target workspace when listing an inherited API. Only the API should be inherited,
		// and not the instances.
		sourceWorkspaceCluster := &clusterapi.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "source-cluster",
			},
		}

		sourceClusterClient := kcpClients.Cluster("source").ClusterV1alpha1().Clusters()

		_, err = sourceClusterClient.Create(ctx, sourceWorkspaceCluster, metav1.CreateOptions{})
		if err != nil {
			t.Errorf("Error creating sourceWorkspaceCluster inside source: %v", err)
			return
		}
		server.Artifact(t, func() (runtime.Object, error) {
			return sourceClusterClient.Get(ctx, "source-cluster", metav1.GetOptions{})
		})

		// Make sure API group from CRD does NOT show up in target workspace group discovery
		groups, err := kcpClients.Cluster("target").Discovery().ServerGroups()
		if err != nil {
			t.Errorf("error retrieving target workspace group discovery: %w", err)
			return
		}
		if groupExists(groups, cluster.GroupName) {
			t.Errorf("should not have seen cluster API group in target workspace group discovery")
			return
		}

		// Update target workspace to inherit from source
		targetWorkspace, err = kcpAdminClient.TenancyV1alpha1().Workspaces().Get(ctx, targetWorkspace.GetName(), metav1.GetOptions{})

		if err != nil {
			t.Errorf("error retrieving target workspace: %w", err)
			return
		}

		targetWorkspace.Spec.InheritFrom = "source"
		if _, err = kcpAdminClient.TenancyV1alpha1().Workspaces().Update(ctx, targetWorkspace, metav1.UpdateOptions{}); err != nil {
			t.Errorf("error updating target workspace to inherit from source: %v", err)
			return
		}

		// Make sure API group from inheritance shows up in target workspace group discovery
		if err := wait.PollImmediateUntilWithContext(ctx, 100*time.Millisecond, func(c context.Context) (done bool, err error) {
			groups, err := kcpClients.Cluster("target").Discovery().ServerGroups()
			if err != nil {
				return false, fmt.Errorf("error retrieving target workspace group discovery: %w", err)
			}
			if groupExists(groups, cluster.GroupName) {
				return true, nil
			}
			return false, nil
		}); err != nil {
			t.Errorf("source workspace discovery is missing group %q", cluster.GroupName)
			return
		}

		// Make sure API resource from inheritance shows up in target workspace group version discovery
		resources, err = kcpClients.Cluster("target").Discovery().ServerResourcesForGroupVersion(clusterapi.SchemeGroupVersion.String())
		if err != nil {
			t.Errorf("error retrieving target workspace cluster API discovery: %v", err)
			return
		}
		if !resourceExists(resources, "clusters") {
			t.Errorf("target workspace discovery is missing clusters resource")
			return
		}

		// Make sure we can perform CRUD operations in the target cluster for the inherited API.

		// Make sure list shows nothing to start

		targetClusterClient := kcpClients.Cluster("target").ClusterV1alpha1().Clusters()
		clusters, err := targetClusterClient.List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Errorf("error listing clusters inside target: %v", err)
			return
		}
		if len(clusters.Items) != 0 {
			t.Errorf("expected 0 clusters inside target but got %d: %#v", len(clusters.Items), clusters.Items)
			return
		}

		targetWorkspaceCluster := &clusterapi.Cluster{
			ObjectMeta: metav1.ObjectMeta{
				Name: "target-cluster",
			},
		}
		if _, err := targetClusterClient.Create(ctx, targetWorkspaceCluster, metav1.CreateOptions{}); err != nil {
			t.Errorf("error creating targetWorkspaceCluster inside target: %v", err)
			return
		}
		server.Artifact(t, func() (runtime.Object, error) {
			return targetClusterClient.Get(ctx, "target-cluster", metav1.GetOptions{})
		})

		// Make sure source has sourceWorkspaceCluster and target has targetWorkspaceCluster
		clusters, err = sourceClusterClient.List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Errorf("error listing clusters inside source: %v", err)
			return
		}
		if len(clusters.Items) != 1 {
			t.Errorf("expected 1 cluster inside source, got %d: %#v", len(clusters.Items), clusters.Items)
			return
		}
		if clusters.Items[0].Name != "source-cluster" {
			t.Errorf("expected source-cluster, got %q", clusters.Items[0].Name)
		}

		clusters, err = targetClusterClient.List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Errorf("error listing clusters inside target: %v", err)
			return
		}
		if len(clusters.Items) != 1 {
			t.Errorf("expected 1 cluster inside target, got %d: %#v", len(clusters.Items), clusters.Items)
			return
		}
		if clusters.Items[0].Name != "target-cluster" {
			t.Errorf("expected target-cluster, got %q", clusters.Items[0].Name)
		}

	}, framework.KcpConfig{
		Name: "main",
		Args: []string{"--install-workspace-scheduler"},
	})
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
