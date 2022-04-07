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

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	nscontroller "github.com/kcp-dev/kcp/pkg/reconciler/workload/namespace"
	"github.com/kcp-dev/kcp/pkg/syncer"
	conditionsapi "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

// TestServerArgs returns the set of kcp args used to start a test
// server using the token auth file from the working tree.
func TestServerArgs() []string {
	return TestServerArgsWithTokenAuthFile("test/e2e/framework/auth-tokens.csv")
}

// TestServerArgsWithTokenAuthFile returns the set of kcp args used to
// start a test server with the given token auth file.
func TestServerArgsWithTokenAuthFile(tokenAuthFile string) []string {
	return []string{
		"--auto-publish-apis",
		"--discovery-poll-interval=5s",
		"--token-auth-file", tokenAuthFile,
		"--run-virtual-workspaces=true",
	}
}

// KcpFixture manages the lifecycle of a set of kcp servers.
//
// Deprecated for use outside this package. Prefer PrivateKcpServer().
type kcpFixture struct {
	Servers map[string]RunningServer
}

// PrivateKcpServer returns a new kcp server fixture managing a new
// server process that is not intended to be shared between tests.
func PrivateKcpServer(t *testing.T, args ...string) RunningServer {
	serverName := "main"
	f := newKcpFixture(t, kcpConfig{
		Name: serverName,
		Args: args,
	})
	return f.Servers[serverName]
}

// SharedKcpServer returns a kcp server fixture intended to be shared
// between tests. A persistent server will be configured if
// `--kubeconfig` or `--use-default-server` is supplied to the test
// runner. Otherwise a test-managed server will be started. Only tests
// that are known to be hermetic are compatible with shared fixture.
func SharedKcpServer(t *testing.T) RunningServer {
	serverName := "shared"
	kubeconfig := TestConfig.Kubeconfig()
	if len(kubeconfig) > 0 {
		// Use a persistent server

		t.Logf("shared kcp server will target configuration %q", kubeconfig)
		server, err := newPersistentKCPServer(serverName, kubeconfig)
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
	f := newKcpFixture(t, kcpConfig{
		Name: serverName,
		Args: TestServerArgsWithTokenAuthFile(tokenAuthFile),
	})
	return f.Servers[serverName]
}

// Deprecated for use outside this package. Prefer PrivateKcpServer().
func newKcpFixture(t *testing.T, cfgs ...kcpConfig) *kcpFixture {
	f := &kcpFixture{}

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

func NewOrganizationFixture(t *testing.T, server RunningServer) (orgClusterName logicalcluster.LogicalCluster) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cfg := server.DefaultConfig(t)
	clusterClient, err := kcpclientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to create kcp cluster client")

	org, err := clusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-org-",
		},
		Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
			Type: "Organization",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create organization workspace")

	t.Cleanup(func() {
		err := clusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, org.Name, metav1.DeleteOptions{})
		if apierrors.IsNotFound(err) {
			return // ignore not found error
		}
		require.NoErrorf(t, err, "failed to delete organization workspace %s", org.Name)
	})

	require.Eventuallyf(t, func() bool {
		ws, err := clusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, org.Name, metav1.GetOptions{})
		require.Falsef(t, apierrors.IsNotFound(err), "workspace %s was deleted", org.Name)
		if err != nil {
			klog.Errorf("failed to get workspace %s: %v", org.Name, err)
			return false
		}
		return ws.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to wait for organization workspace %s to become ready", org.Name)

	return tenancyv1alpha1.RootCluster.Join(org.Name)
}

func NewWorkspaceFixture(t *testing.T, server RunningServer, orgClusterName logicalcluster.LogicalCluster, workspaceType string) (clusterName logicalcluster.LogicalCluster) {
	schedulable := workspaceType == "Universal"
	return NewWorkspaceWithWorkloads(t, server, orgClusterName, workspaceType, schedulable)
}

func NewWorkspaceWithWorkloads(t *testing.T, server RunningServer, orgClusterName logicalcluster.LogicalCluster, workspaceType string, schedulable bool) (clusterName logicalcluster.LogicalCluster) {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

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
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to wait for workspace %s to become ready", orgClusterName.Join(ws.Name))

	return orgClusterName.Join(ws.Name)
}

// SyncerFixture contains the information to run a syncer fixture.
type SyncerFixture struct {
	RunningServer       RunningServer
	KubeClient          kubernetes.Interface
	WorkloadClusterName string

	upstreamConfig       *rest.Config
	downstreamConfig     *rest.Config
	resources            sets.String
	orgClusterName       logicalcluster.LogicalCluster
	workspaceClusterName logicalcluster.LogicalCluster
}

// NewSyncerFixture creates a downstream server (fakeWorkloadServer), and then creates a workloadClusters on the provided upstream server
// returns a SyncerFixture with the downstream server information, and its kubeclient.
func NewSyncerFixture(
	t *testing.T,
	resources sets.String,
	upstream RunningServer,
	orgClusterName logicalcluster.LogicalCluster,
	wsClusterName logicalcluster.LogicalCluster,
) *SyncerFixture {
	downstreamServer := NewFakeWorkloadServer(t, upstream, orgClusterName)

	upstreamKcpClusterClient, err := kcpclientset.NewClusterForConfig(upstream.DefaultConfig(t))
	require.NoError(t, err)

	workloadCluster, err := CreateWorkloadCluster(t, upstream.Artifact, upstreamKcpClusterClient.Cluster(wsClusterName), downstreamServer)
	require.NoError(t, err)

	upstreamConfig := upstream.DefaultConfig(t)
	downstreamConfig := downstreamServer.DefaultConfig(t)

	downstreamKubeClient, err := kubernetesclientset.NewForConfig(downstreamConfig)
	require.NoError(t, err)

	return &SyncerFixture{
		RunningServer:        downstreamServer,
		KubeClient:           downstreamKubeClient,
		upstreamConfig:       upstreamConfig,
		downstreamConfig:     downstreamConfig,
		resources:            resources,
		orgClusterName:       logicalcluster.From(workloadCluster),
		WorkloadClusterName:  workloadCluster.Name,
		workspaceClusterName: wsClusterName,
	}
}

// WaitForClusterReadyReason waits for the cluster to be ready with the given reason.
func (sf *SyncerFixture) WaitForClusterReadyReason(t *testing.T, ctx context.Context, reason string) {
	sourceKcpClusterClient, err := kcpclient.NewClusterForConfig(sf.upstreamConfig)
	require.NoError(t, err)
	kcpClient := sourceKcpClusterClient.Cluster(sf.workspaceClusterName)

	t.Logf("Waiting for cluster %q condition %q to have reason %q", sf.WorkloadClusterName, conditionsapi.ReadyCondition, reason)
	require.Eventually(t, func() bool {

		cluster, err := kcpClient.WorkloadV1alpha1().WorkloadClusters().Get(ctx, sf.WorkloadClusterName, metav1.GetOptions{})
		if err != nil {
			t.Errorf("Error getting cluster %q: %v", sf.WorkloadClusterName, err)
			return false
		}

		// A reason is only supplied to indicate why a cluster is 'not ready'
		wantReady := len(reason) == 0
		if wantReady {
			return conditions.IsTrue(cluster, conditionsapi.ReadyCondition)
		} else {
			conditionReason := conditions.GetReason(cluster, conditionsapi.ReadyCondition)
			return conditions.IsFalse(cluster, conditionsapi.ReadyCondition) && reason == conditionReason
		}

	}, wait.ForeverTestTimeout, time.Millisecond*100)
	t.Logf("Cluster %q condition %s has reason %q", conditionsapi.ReadyCondition, sf.WorkloadClusterName, reason)
}

// Start starts the Syncer.
func (sf *SyncerFixture) Start(t *testing.T, ctx context.Context) {
	err := syncer.StartSyncer(ctx, sf.upstreamConfig, sf.downstreamConfig, sf.resources, sf.orgClusterName, sf.WorkloadClusterName, 2)
	require.NoError(t, err, "syncer failed to start")
}
