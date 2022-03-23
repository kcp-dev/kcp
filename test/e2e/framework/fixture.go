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

package framework

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	nscontroller "github.com/kcp-dev/kcp/pkg/reconciler/namespace"
	"github.com/kcp-dev/kcp/pkg/syncer"
)

// KcpFixture manages the lifecycle of a set of kcp servers.
type KcpFixture struct {
	Servers map[string]RunningServer
}

// SharedKcpServer returns a kcp server fixture intended to be shared
// between tests. A persistent server will be configured if
// `--kubeconfig` is supplied to the test runner. Otherwise a
// test-managed server will be started. Only tests that are known to
// be hermetic are compatible with shared fixture.
func SharedKcpServer(t *testing.T) RunningServer {
	serverName := "shared"
	if len(TestConfig.KubeConfig) > 0 {
		// Use a persistent server

		t.Logf("shared kcp server will target the persistent kcp server identified by --kubeconfig")
		server, err := newPersistentKCPServer(serverName, TestConfig.KubeConfig)
		require.NoError(t, err, "failed to create persistent server fixture")
		return server
	}

	// Use a test-provisioned server
	//
	// TODO(marun) Enable non-persistent fixture to be shared across
	// tests. This will likely require composing tests into a suite that
	// initializes the shared fixture before tests that rely on the
	// fixture.

	tokenAuthFile := WriteTokenAuthFile(t)
	f := NewKcpFixture(t, KcpConfig{
		Name: serverName,
		Args: []string{
			"--auto-publish-apis",
			"--discovery-poll-interval=2s",
			"--token-auth-file", tokenAuthFile,
			"--run-virtual-workspaces=true",
		},
	})
	return f.Servers[serverName]
}

func NewKcpFixture(t *testing.T, cfgs ...KcpConfig) *KcpFixture {
	f := &KcpFixture{}

	artifactDir, dataDir, err := ScratchDirs(t)
	require.NoError(t, err, "failed to create scratch dirs: %v", err)

	// Initialize servers from the provided configuration
	var servers []*kcpServer
	f.Servers = map[string]RunningServer{}
	for _, cfg := range cfgs {
		server, err := newKcpServer(t, cfg, artifactDir, dataDir)
		require.NoError(t, err)

		servers = append(servers, server)
		f.Servers[server.name] = server
	}

	// Launch kcp servers and ensure they are ready before starting the test
	start := time.Now()
	t.Log("Starting kcp servers...")
	wg := sync.WaitGroup{}
	wg.Add(len(servers))
	for i, srv := range servers {
		var opts []RunOption
		if LogToConsoleEnvSet() || cfgs[i].LogToConsole {
			opts = append(opts, WithLogStreaming)
		}
		if InProcessEnvSet() || cfgs[i].RunInProcess {
			opts = append(opts, RunInProcess)
		}
		err := srv.Run(opts...)
		require.NoError(t, err)

		// Wait for the server to become ready
		go func(s *kcpServer, i int) {
			defer wg.Done()
			err := s.Ready(!cfgs[i].RunInProcess)
			require.NoError(t, err, "kcp server %s never became ready: %v", s.name, err)
		}(srv, i)
	}
	wg.Wait()

	if t.Failed() {
		t.Fatal("Fixture setup failed: one or more servers did not become ready")
	}

	t.Logf("Started kcp servers after %s", time.Since(start))

	return f
}

func InProcessEnvSet() bool {
	inProcess, _ := strconv.ParseBool(os.Getenv("INPROCESS"))
	return inProcess
}

func LogToConsoleEnvSet() bool {
	inProcess, _ := strconv.ParseBool(os.Getenv("LOG_TO_CONSOLE"))
	return inProcess
}

func NewOrganizationFixture(t *testing.T, server RunningServer) (orgClusterName string) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cfg := server.DefaultConfig(t)
	clusterClient, err := kcpclientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to create kcp cluster client")

	org, err := clusterClient.Cluster(helper.RootCluster).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-org-",
		},
		Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
			Type: "Organization",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create organization workspace")

	t.Cleanup(func() {
		err := clusterClient.Cluster(helper.RootCluster).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, org.Name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return // ignore not found error
		}
		require.NoErrorf(t, err, "failed to delete organization workspace %s", org.Name)
	})

	require.Eventuallyf(t, func() bool {
		ws, err := clusterClient.Cluster(helper.RootCluster).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, org.Name, metav1.GetOptions{})
		require.Falsef(t, apierrors.IsNotFound(err), "workspace %s was deleted", org.Name)
		if err != nil {
			klog.Errorf("failed to get workspace %s: %v", org.Name, err)
			return false
		}
		return ws.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to wait for organization workspace %s to become ready", org.Name)

	return helper.EncodeOrganizationAndClusterWorkspace(helper.RootCluster, org.Name)
}

func NewWorkspaceFixture(t *testing.T, server RunningServer, orgClusterName string, workspaceType string) (clusterName string) {
	schedulable := workspaceType == "Universal"
	return NewWorkspaceWithWorkloads(t, server, orgClusterName, workspaceType, schedulable)
}

func NewWorkspaceWithWorkloads(t *testing.T, server RunningServer, orgClusterName string, workspaceType string, schedulable bool) (clusterName string) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	rootOrg, orgName, err := helper.ParseLogicalClusterName(orgClusterName)
	require.NoErrorf(t, err, "failed to parse organization cluster name %q", orgClusterName)
	require.Equalf(t, rootOrg, helper.RootCluster, "expected an org cluster name, i.e. with \"%s:\" prefix", helper.RootCluster)

	cfg := server.DefaultConfig(t)
	clusterClient, err := kcpclientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct client for server")

	labels := map[string]string{}
	if !schedulable {
		labels[nscontroller.WorkspaceSchedulableLabel] = "false"
	}

	ws, err := clusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-workspace-",
			Labels:       labels,
		},
		Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
			Type: workspaceType,
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create workspace")

	t.Cleanup(func() {
		err := clusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, ws.Name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return // ignore not found error
		}
		require.NoErrorf(t, err, "failed to delete workspace %s", ws.Name)
	})

	require.Eventuallyf(t, func() bool {
		ws, err := clusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		require.Falsef(t, apierrors.IsNotFound(err), "workspace %s was deleted", ws.Name)
		if err != nil {
			klog.Errorf("failed to get workspace %s: %v", ws.Name, err)
			return false
		}
		return ws.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to wait for workspace %s:%s to become ready", orgName, ws.Name)

	return helper.EncodeOrganizationAndClusterWorkspace(orgName, ws.Name)
}

// StartWorkspaceSyncer starts a new syncer to synchronize the identified set of
// resource types scheduled for the given workload cluster from the upstream server
// to the downstream server.
func StartWorkspaceSyncer(
	t *testing.T,
	resources sets.String,
	workloadCluster *workloadv1alpha1.WorkloadCluster,
	upstream, downstream RunningServer,
) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	upstreamConfig := upstream.DefaultConfig(t)
	downstreamConfig := downstream.DefaultConfig(t)

	err := syncer.StartSyncer(ctx, upstreamConfig, downstreamConfig, resources, workloadCluster.ClusterName, workloadCluster.Name, 2)
	require.NoError(t, err, "syncer failed to start")
}
