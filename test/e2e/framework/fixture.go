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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	workloadcliplugin "github.com/kcp-dev/kcp/pkg/cliplugins/workload/plugin"
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
		"--feature-gates=KCPLocationAPI=true",
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
// `--kcp-kubeconfig` or `--use-default-kcp-server` is supplied to the test
// runner. Otherwise a test-managed server will be started. Only tests
// that are known to be hermetic are compatible with shared fixture.
func SharedKcpServer(t *testing.T) RunningServer {
	serverName := "shared"
	kubeconfig := TestConfig.KCPKubeconfig()
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

// SyncerFixture configures a syncer fixture. Its `Start` method does the work of starting a syncer.
type SyncerFixture struct {
	ResourcesToSync      sets.String
	UpstreamServer       RunningServer
	WorkspaceClusterName logicalcluster.LogicalCluster
	WorkloadClusterName  string
	InstallCRDs          func(config *rest.Config, isLogicalCluster bool)
}

// SetDefaults ensures a valid configuration even if not all values are explicitly provided.
func (sf *SyncerFixture) setDefaults() {
	// Default configuration to avoid tests having to be exaustive
	if len(sf.WorkloadClusterName) == 0 {
		// This only needs to vary when more than one syncer need to be tested in a workspace
		sf.WorkloadClusterName = "pcluster-01"
	}
	if sf.ResourcesToSync == nil {
		// resources-to-sync is additive to the core set of resources so not providing any
		// values means default types will still be synced.
		sf.ResourcesToSync = sets.NewString()
	}
}

// Start starts a new syncer against the given upstream kcp workspace. Whether the syncer run
// in-process or deployed on a pcluster will depend whether --pcluster-kubeconfig and
// --syncer-image are supplied to the test invocation.
func (sf SyncerFixture) Start(t *testing.T) *StartedSyncerFixture {
	sf.setDefaults()

	// Write the upstream logical cluster config to disk for the workspace plugin
	upstreamRawConfig, err := sf.UpstreamServer.RawConfig()
	require.NoError(t, err)
	_, kubeconfigPath := writeLogicalClusterConfig(t, upstreamRawConfig, sf.WorkspaceClusterName)

	useDeployedSyncer := len(TestConfig.PClusterKubeconfig()) > 0

	syncerImage := TestConfig.SyncerImage()
	if useDeployedSyncer {
		require.NotZero(t, len(syncerImage), "--syncer-image must be specified if testing with a deployed syncer")
	} else {
		// The image needs to be a non-empty string for the plugin command but the value
		// doesn't matter if not deploying a syncer.
		syncerImage = "not-a-valid-image"
	}

	// Run the plugin command to enable the syncer and collect the resulting yaml
	t.Logf("Configuring workspace %s for syncing", sf.WorkspaceClusterName)
	pluginArgs := []string{
		"workload",
		"sync",
		sf.WorkloadClusterName,
		"--syncer-image", syncerImage,
	}
	for _, resource := range sf.ResourcesToSync.List() {
		pluginArgs = append(pluginArgs, "--resources", resource)
	}
	syncerYAML := RunKcpCliPlugin(t, kubeconfigPath, pluginArgs)

	var downstreamConfig *rest.Config
	var downstreamKubeconfigPath string
	if useDeployedSyncer {
		// The syncer will target the pcluster identified by `--pcluster-kubeconfig`.
		downstreamKubeconfigPath = TestConfig.PClusterKubeconfig()
		fs, err := os.Stat(downstreamKubeconfigPath)
		require.NoError(t, err)
		require.NotZero(t, fs.Size(), "%s points to an empty file", downstreamKubeconfigPath)
		rawConfig, err := clientcmd.LoadFromFile(downstreamKubeconfigPath)
		require.NoError(t, err, "failed to load pcluster kubeconfig")
		config := clientcmd.NewNonInteractiveClientConfig(*rawConfig, rawConfig.CurrentContext, nil, nil)
		downstreamConfig, err = config.ClientConfig()
		require.NoError(t, err)
	} else {
		// The syncer will target a logical cluster that is a peer to the current workspace. A
		// logical server provides as a lightweight approximation of a pcluster for tests that
		// don't need to validate running workloads or interaction with kube controllers.
		parentClusterName, ok := sf.WorkspaceClusterName.Parent()
		require.True(t, ok, "%s does not have a parent", sf.WorkspaceClusterName)
		downstreamServer := NewFakeWorkloadServer(t, sf.UpstreamServer, parentClusterName)
		downstreamConfig = downstreamServer.DefaultConfig(t)
		downstreamKubeconfigPath = downstreamServer.KubeconfigPath()
	}

	if sf.InstallCRDs != nil {
		// Attempt crd installation to ensure the downstream server has an api surface
		// compatible with the test.
		sf.InstallCRDs(downstreamConfig, !useDeployedSyncer)
	}

	// Apply the yaml output from the plugin to the downstream server
	KubectlApply(t, downstreamKubeconfigPath, syncerYAML)

	// Extract the configuration for an in-process syncer from the resources that were
	// applied to the downstream server. This maximizes the parity between the
	// configuration of a deployed and in-process syncer.
	syncerNamespace := workloadcliplugin.GetSyncerID(sf.WorkspaceClusterName.String(), sf.WorkloadClusterName)
	syncerConfig := syncerConfigFromCluster(t, downstreamConfig, syncerNamespace)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	downstreamKubeClient, err := kubernetesclientset.NewForConfig(downstreamConfig)
	require.NoError(t, err)

	if useDeployedSyncer {
		// Ensure cleanup of pcluster resources

		syncerID := syncerConfig.ID()
		t.Cleanup(func() {
			t.Logf("Deleting syncer resources for logical cluster %q, workload cluster %q", syncerConfig.KCPClusterName, syncerConfig.WorkloadClusterName)
			err := downstreamKubeClient.CoreV1().Namespaces().Delete(ctx, syncerID, metav1.DeleteOptions{})
			if err != nil {
				t.Errorf("failed to delete Namespace %q: %v", syncerID, err)
			}
			err = downstreamKubeClient.RbacV1().ClusterRoleBindings().Delete(ctx, syncerID, metav1.DeleteOptions{})
			if err != nil {
				t.Errorf("failed to delete ClusterRoleBinding %q: %v", syncerID, err)
			}
			err = downstreamKubeClient.RbacV1().ClusterRoles().Delete(ctx, syncerID, metav1.DeleteOptions{})
			if err != nil {
				t.Errorf("failed to delete ClusterRole %q: %v", syncerID, err)
			}

			t.Logf("Deleting synced resources for logical cluster %q, workload cluster %q", syncerConfig.KCPClusterName, syncerConfig.WorkloadClusterName)
			namespaces, err := downstreamKubeClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
			if err != nil {
				t.Errorf("failed to list namespaces: %v", err)
			}
			for _, ns := range namespaces.Items {
				locator, err := syncer.LocatorFromAnnotations(ns.Annotations)
				if err != nil {
					t.Errorf("failed to retrieve locator from ns %q: %v", ns.Name, err)
					continue
				}
				if locator == nil {
					// Not a kcp-synced namespace
					continue
				}
				if locator.LogicalCluster != syncerConfig.KCPClusterName {
					// Not a namespace synced by this syncer
					continue
				}
				err = downstreamKubeClient.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{})
				if err != nil {
					t.Errorf("failed to delete Namespace %q: %v", ns.Name, err)
				}
			}
		})

	} else {
		// Start an in-process syncer

		err := syncer.StartSyncer(ctx, syncerConfig, 2, 5*time.Second)
		require.NoError(t, err, "syncer failed to start")
	}

	startedSyncer := &StartedSyncerFixture{
		SyncerConfig:         syncerConfig,
		DownstreamConfig:     downstreamConfig,
		DownstreamKubeClient: downstreamKubeClient,
	}

	// The workload cluster becoming ready indicates the syncer is healthy and has
	// successfully sent a heartbeat to kcp.
	startedSyncer.WaitForClusterReadyReason(t, ctx, "")

	return startedSyncer
}

// StartedSyncerFixture contains the configuration used to start a syncer and interact with its
// downstream cluster.
type StartedSyncerFixture struct {
	SyncerConfig *syncer.SyncerConfig

	// Provide cluster-admin config and client for test purposes. The downstream config in
	// SyncerConfig will be less privileged.
	DownstreamConfig     *rest.Config
	DownstreamKubeClient kubernetes.Interface
}

// WaitForClusterReadyReason waits for the cluster to be ready with the given reason.
func (sf *StartedSyncerFixture) WaitForClusterReadyReason(t *testing.T, ctx context.Context, reason string) {
	cfg := sf.SyncerConfig

	t.Logf("Waiting for cluster %q condition %q to have reason %q", cfg.WorkloadClusterName, conditionsapi.ReadyCondition, reason)
	kcpClusterClient, err := kcpclient.NewClusterForConfig(cfg.UpstreamConfig)
	require.NoError(t, err)
	kcpClient := kcpClusterClient.Cluster(cfg.KCPClusterName)
	require.Eventually(t, func() bool {
		cluster, err := kcpClient.WorkloadV1alpha1().WorkloadClusters().Get(ctx, cfg.WorkloadClusterName, metav1.GetOptions{})
		if err != nil {
			t.Errorf("Error getting cluster %q: %v", cfg.WorkloadClusterName, err)
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
	if len(reason) == 0 {
		t.Logf("Cluster %q is %s", cfg.WorkloadClusterName, conditionsapi.ReadyCondition)
	} else {
		t.Logf("Cluster %q condition %s has reason %q", conditionsapi.ReadyCondition, cfg.WorkloadClusterName, reason)
	}
}

// writeLogicalClusterConfig creates a logical cluster config for the given config and
// cluster name and writes it to the test's artifact path. Useful for configuring the
// workspace plugin with --kubeconfig.
func writeLogicalClusterConfig(t *testing.T, rawConfig clientcmdapi.Config, clusterName logicalcluster.LogicalCluster) (clientcmd.ClientConfig, string) {
	logicalRawConfig := LogicalClusterRawConfig(rawConfig, clusterName)
	artifactDir, err := CreateTempDirForTest(t, "artifacts")
	require.NoError(t, err)
	pathSafeClusterName := strings.ReplaceAll(clusterName.String(), ":", "_")
	kubeconfigPath := filepath.Join(artifactDir, fmt.Sprintf("%s.kubeconfig", pathSafeClusterName))
	err = clientcmd.WriteToFile(logicalRawConfig, kubeconfigPath)
	require.NoError(t, err)
	logicalConfig := clientcmd.NewNonInteractiveClientConfig(logicalRawConfig, logicalRawConfig.CurrentContext, &clientcmd.ConfigOverrides{}, nil)
	return logicalConfig, kubeconfigPath
}

// syncerConfigFromCluster reads the configuration needed to start an in-process
// syncer from the resources applied to a cluster for a deployed syncer.
func syncerConfigFromCluster(t *testing.T, config *rest.Config, namespace string) *syncer.SyncerConfig {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	kubeClient, err := kubernetesclientset.NewForConfig(config)
	require.NoError(t, err)

	// Read the upstream kubeconfig from the syncer secret
	secret, err := kubeClient.CoreV1().Secrets(namespace).Get(ctx, workloadcliplugin.SyncerSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	upstreamConfigBytes := secret.Data[workloadcliplugin.SyncerSecretConfigKey]
	require.NotEmpty(t, upstreamConfigBytes, "upstream config is required")
	upstreamConfig, err := clientcmd.RESTConfigFromKubeConfig(upstreamConfigBytes)
	require.NoError(t, err, "failed to load upstream config")

	// Read the arguments from the syncer deployment
	deployment, err := kubeClient.AppsV1().Deployments(namespace).Get(ctx, workloadcliplugin.SyncerResourceName, metav1.GetOptions{})
	require.NoError(t, err)
	containers := deployment.Spec.Template.Spec.Containers
	require.NotEmpty(t, containers, "expected at least one container in syncer deployment")
	argMap, err := syncerArgsToMap(containers[0].Args)
	require.NoError(t, err)

	require.NotEmpty(t, argMap["--workload-cluster-name"], "--workload-cluster-name is required")
	workloadClusterName := argMap["--workload-cluster-name"][0]
	require.NotEmpty(t, workloadClusterName, "a value for --workload-cluster-name is required")

	require.NotEmpty(t, argMap["--from-cluster"], "--workload-cluster-name is required")
	fromCluster := argMap["--from-cluster"][0]
	require.NotEmpty(t, fromCluster, "a value for --from-cluster is required")
	kcpClusterName := logicalcluster.New(fromCluster)

	resourcesToSync := argMap["--resources"]
	require.NotEmpty(t, fromCluster, "--resources is required")

	// Read the downstream token from the deployment's service account secret
	var tokenSecret corev1.Secret
	require.Eventually(t, func() bool {
		secrets, err := kubeClient.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Errorf("failed to list secrets: %v", err)
			return false
		}
		for _, secret := range secrets.Items {
			if secret.Annotations[corev1.ServiceAccountNameKey] == workloadcliplugin.SyncerResourceName {
				tokenSecret = secret
				return true
			}
		}
		return false
	}, wait.ForeverTestTimeout, time.Millisecond*100, "token secret for syncer service account not found")
	token := tokenSecret.Data["token"]
	require.NotEmpty(t, token, "token is required")

	// Compose a new downstream config that uses the token
	downstreamConfig := rest.CopyConfig(config)
	downstreamConfig.BearerToken = string(token)

	return &syncer.SyncerConfig{
		UpstreamConfig:      upstreamConfig,
		DownstreamConfig:    downstreamConfig,
		ResourcesToSync:     sets.NewString(resourcesToSync...),
		KCPClusterName:      kcpClusterName,
		WorkloadClusterName: workloadClusterName,
	}
}

// syncerArgsToMap converts the cli argument list from a syncer deployment into a map
// keyed by flags.
func syncerArgsToMap(args []string) (map[string][]string, error) {
	argMap := map[string][]string{}
	for _, arg := range args {
		argParts := strings.Split(arg, "=")
		if len(argParts) != 2 {
			return nil, fmt.Errorf("arg %q isn't of the expected form `<key>=<value>`", arg)
		}
		key, value := argParts[0], argParts[1]
		if _, ok := argMap[key]; !ok {
			argMap[key] = []string{value}
		} else {
			argMap[key] = append(argMap[key], value)
		}
	}
	return argMap, nil
}

// KcpCliPluginCommand returns the cli args to run the workspace plugin directly or
// via go run (depending on whether NO_GORUN is set).
func KcpCliPluginCommand() []string {
	if NoGoRunEnvSet() {
		return []string{"kubectl", "kcp"}

	} else {
		cmdPath := filepath.Join(RepositoryDir(), "cmd", "kubectl-kcp")
		return []string{"go", "run", cmdPath}
	}
}

// RunKcpCliPlugin runs the kcp workspace plugin with the provided subcommand and
// returns the combined stderr and stdout output.
func RunKcpCliPlugin(t *testing.T, kubeconfigPath string, subcommand []string) []byte {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cmdParts := append(KcpCliPluginCommand(), subcommand...)
	cmd := exec.CommandContext(ctx, cmdParts[0], cmdParts[1:]...)

	cmd.Env = os.Environ()
	// TODO(marun) Consider configuring the workspace plugin with args instead of this env
	cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", kubeconfigPath))

	t.Logf("running: KUBECONFIG=%s %s", kubeconfigPath, strings.Join(cmdParts, " "))

	output, err := cmd.CombinedOutput()
	t.Logf("kcp plugin output:\n%s", output)
	require.NoError(t, err, "error running kcp plugin command")
	return output
}

// KubectlApply runs kubectl apply -f with the supplied input piped to stdin and returns
// the combined stderr and stdout output.
func KubectlApply(t *testing.T, kubeconfigPath string, input []byte) []byte {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cmdParts := []string{"kubectl", "--kubeconfig", kubeconfigPath, "apply", "-f", "-"}
	cmd := exec.CommandContext(ctx, cmdParts[0], cmdParts[1:]...)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	_, err = stdin.Write(input)
	require.NoError(t, err)
	// Close to ensure kubectl doesn't keep waiting for input
	err = stdin.Close()
	require.NoError(t, err)

	t.Logf("running: %s", strings.Join(cmdParts, " "))

	output, err := cmd.CombinedOutput()
	t.Logf("kubectl apply output:\n%s", output)
	require.NoError(t, err)

	return output
}
