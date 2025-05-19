/*
Copyright 2025 The KCP Authors.

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

package server

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/egymgmbh/go-prefix-writer/prefixer"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"sigs.k8s.io/yaml"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpscheme "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/scheme"
	"github.com/kcp-dev/kcp/sdk/testing/env"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
)

// kcpBinariesDirEnvDir can be set to find kcp binaries for testing.
const kcpBinariesDirEnvDir = "KCP_BINARIES_DIR"

// RunInProcessFunc instantiates the kcp server in process for easier debugging.
// It is here to decouple the rest of the code from kcp core dependencies.
// Deprecated: Use ContextRunInProcessFunc instead.
var RunInProcessFunc func(t TestingT, dataDir string, args []string) (<-chan struct{}, error)

type KcpRunner func(context.Context, TestingT, Config) (<-chan struct{}, error)

// ContextRunInProcessFunc instantiates the kcp server in process for easier debugging.
// It is here to decouple the rest of the code from kcp core dependencies.
var ContextRunInProcessFunc KcpRunner = func(ctx context.Context, t TestingT, cfg Config) (<-chan struct{}, error) {
	return nil, fmt.Errorf("not implemented")
}

// Fixture manages the lifecycle of a set of kcp servers.
//
// Deprecated for use outside this package. Prefer PrivateKcpServer().
type Fixture = map[string]RunningServer

// NewFixture returns a new kcp server fixture.
func NewFixture(t TestingT, cfgs ...Config) Fixture {
	t.Helper()

	// Initialize servers from the provided configuration
	servers := make([]*kcpServer, 0, len(cfgs))
	ret := make(Fixture, len(cfgs))
	for _, cfg := range cfgs {
		if len(cfg.ArtifactDir) == 0 {
			panic(fmt.Sprintf("provided kcpConfig for %s is incorrect, missing ArtifactDir", cfg.Name))
		}
		if len(cfg.DataDir) == 0 {
			panic(fmt.Sprintf("provided kcpConfig for %s is incorrect, missing DataDir", cfg.Name))
		}
		srv, err := newKcpServer(t, cfg)
		require.NoError(t, err)

		servers = append(servers, srv)
		ret[srv.Name()] = srv
	}

	// Launch kcp servers and ensure they are ready before starting the test
	start := time.Now()
	t.Log("Starting kcp servers...")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	g, ctx := errgroup.WithContext(ctx)
	for i, srv := range servers {
		err := srv.Run(t)
		require.NoError(t, err)

		// Wait for the server to become ready
		g.Go(func() error {
			if err := srv.loadCfg(ctx); err != nil {
				// Cancel the context to kill all goroutines - if any
				// server failed to setup properly the setup quits
				// anyhow.
				cancel()
				return err
			}

			rootCfg := srv.RootShardSystemMasterBaseConfig(t)
			t.Logf("Waiting for readiness for server at %s", rootCfg.Host)
			if err := WaitForReady(ctx, rootCfg); err != nil {
				cancel()
				return err
			}

			if !cfgs[i].RunInProcess {
				rootCfg := srv.RootShardSystemMasterBaseConfig(t)
				MonitorEndpoints(t, rootCfg, "/livez", "/readyz")
			}

			return nil
		})
	}
	err := g.Wait()
	require.NoError(t, err, "failed to start kcp servers")

	for _, s := range servers {
		scrapeMetricsForServer(t, s)
	}

	if t.Failed() {
		t.Fatal("Fixture setup failed: one or more servers did not become ready")
	}

	t.Cleanup(func() {
		t.Logf("Gathering metrics from kcp servers...")
		ctx, cancel := context.WithTimeout(ctx, wait.ForeverTestTimeout)
		defer cancel()

		for _, s := range servers {
			t.Log("Gathering metrics for kcp server", s.Name())
			gatherMetrics(ctx, t, s, s.cfg.ArtifactDir)
		}
	})

	t.Logf("Started kcp servers after %s", time.Since(start))

	return ret
}

// kcpServer exposes a kcp invocation to a test and
// ensures the following semantics:
//   - the server will run only until the test deadline
//   - all ports and data directories are unique to support
//     concurrent execution within a test case and across tests
type kcpServer struct {
	cfg              Config
	lock             *sync.Mutex
	clientCfg        clientcmd.ClientConfig
	cancel           func()
	shutdownComplete bool
}

func newKcpServer(t TestingT, cfg Config) (*kcpServer, error) {
	t.Helper()

	s := &kcpServer{
		cfg:  cfg,
		lock: &sync.Mutex{},
	}

	s.cfg.ArtifactDir = filepath.Join(s.cfg.ArtifactDir, "kcp", cfg.Name)
	if err := os.MkdirAll(s.cfg.ArtifactDir, 0755); err != nil {
		return nil, fmt.Errorf("could not create artifact dir: %w", err)
	}

	s.cfg.DataDir = filepath.Join(s.cfg.DataDir, "kcp", cfg.Name)
	if err := os.MkdirAll(s.cfg.DataDir, 0755); err != nil {
		return nil, fmt.Errorf("could not create data dir: %w", err)
	}

	kcpListenPort, err := GetFreePort(t)
	if err != nil {
		return nil, err
	}
	etcdClientPort, err := GetFreePort(t)
	if err != nil {
		return nil, err
	}
	etcdPeerPort, err := GetFreePort(t)
	if err != nil {
		return nil, err
	}

	s.cfg.Args = append(
		[]string{
			"--root-directory",
			s.cfg.DataDir,
			"--secure-port=" + kcpListenPort,
			"--embedded-etcd-client-port=" + etcdClientPort,
			"--embedded-etcd-peer-port=" + etcdPeerPort,
			"--embedded-etcd-wal-size-bytes=" + strconv.Itoa(5*1000), // 5KB
			"--kubeconfig-path=" + s.KubeconfigPath(),
			"--feature-gates=" + fmt.Sprintf("%s", utilfeature.DefaultFeatureGate),
			"--audit-log-path", filepath.Join(s.cfg.ArtifactDir, "kcp.audit"),
			"--v=4",
		},
		s.cfg.Args...,
	)

	return s, nil
}

// StartKcpCommand returns the string tokens required to start kcp in
// the currently configured mode (direct or via `go run`).
func StartKcpCommand(identity string) []string {
	command := Command("kcp", identity)
	return append(command, "start")
}

// Command returns the string tokens required to start
// the given executable in the currently configured mode (direct or
// via `go run`).
func Command(executableName, identity string) []string {
	if env.RunDelveEnvSet() {
		cmdPath := filepath.Join(kcptestinghelpers.RepositoryDir(), "cmd", executableName)
		return []string{"dlv", "debug", "--api-version=2", "--headless", fmt.Sprintf("--listen=unix:dlv-%s.sock", identity), cmdPath, "--"}
	}

	// are we inside of the kcp repository?
	repo := kcptestinghelpers.RepositoryDir()
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	inKcp := strings.HasPrefix(wd, repo+"/")

	binary := executableName
	if binDir := os.Getenv(kcpBinariesDirEnvDir); binDir == "" && inKcp {
		binary = filepath.Join(kcptestinghelpers.RepositoryBinDir(), executableName)
	} else if binDir != "" {
		binary = filepath.Join(binDir, executableName)
	}

	if env.NoGoRunEnvSet() || !inKcp {
		return []string{binary}
	}

	return []string{"go", "run", filepath.Join(repo, "cmd", executableName)}
}

// Run runs the kcp server while the parent context is active. This call is not blocking,
// callers should ensure that the server is Ready() before using it.
func (c *kcpServer) Run(t TestingT) error {
	c.lock.Lock()
	defer c.lock.Unlock()

	var runner KcpRunner = runExternal
	if c.cfg.RunInProcess {
		if RunInProcessFunc == nil {
			// No RunInProcessFunc set, can safely default to context
			// variant
			runner = ContextRunInProcessFunc
		} else {
			runner = func(ctx context.Context, t TestingT, cfg Config) (<-chan struct{}, error) {
				t.Log("RunInProcessFunc is deprecated, please migrate to ContextRunInProcessFunc")
				t.Log("RunInProcessFunc is deprecated, stopping the server will not work")
				return RunInProcessFunc(t, cfg.DataDir, cfg.Args)
			}
		}
	}
	if runner == nil {
		return fmt.Errorf("runner is nil")
	}

	ctx, ctxCancel := context.WithCancel(context.Background())

	shutdownComplete, err := runner(ctx, t, c.cfg)
	if err != nil {
		ctxCancel()
		return err
	}

	c.cancel = func() {
		t.Log("cleanup: canceling context")
		ctxCancel()

		// Wait for the kcp server to stop
		t.Log("cleanup: waiting for shutdownComplete")
		<-shutdownComplete
		c.lock.Lock()
		c.shutdownComplete = true
		c.lock.Unlock()
		t.Log("cleanup: received shutdownComplete")
	}
	t.Cleanup(c.cancel)

	return nil
}

func (c *kcpServer) Stop() {
	c.lock.Lock()
	defer c.lock.Unlock()

	if c.cancel == nil {
		return
	}
	c.cancel()
}

func (c *kcpServer) Stopped() bool {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.shutdownComplete
}

func runExternal(ctx context.Context, t TestingT, cfg Config) (<-chan struct{}, error) {
	commandLine := append(StartKcpCommand("KCP"), cfg.Args...)

	t.Logf("running: %v", strings.Join(commandLine, " "))

	// NOTE: do not use exec.CommandContext here. That method issues a SIGKILL when the context is done, and we
	// want to issue SIGTERM instead, to give the server a chance to shut down cleanly.
	cmd := exec.Command(commandLine[0], commandLine[1:]...) //nolint:gosec // G204: This is a test utility with controlled inputs

	// Create a new process group for the child/forked process (which is either 'go run ...' or just 'kcp
	// ...'). This is necessary so the SIGTERM we send to terminate the kcp server works even with the
	// 'go run' variant - we have to work around this issue: https://github.com/golang/go/issues/40467.
	// Thanks to
	// https://medium.com/@felixge/killing-a-child-process-and-all-of-its-children-in-go-54079af94773 for
	// the idea!
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	logFile, err := os.Create(filepath.Join(cfg.ArtifactDir, "kcp.log"))
	if err != nil {
		return nil, fmt.Errorf("could not create log file: %w", err)
	}

	// Closing the logfile is necessary so the cmd.Wait() call in the goroutine below can finish (it only finishes
	// waiting when the internal io.Copy goroutines for stdin/stdout/stderr are done, and that doesn't happen if
	// the log file remains open.
	t.Cleanup(func() {
		logFile.Close()
	})

	log := bytes.Buffer{}

	writers := []io.Writer{&log, logFile}

	if cfg.LogToConsole {
		prefix := fmt.Sprintf("%s: ", t.Name())
		writers = append(writers, prefixer.New(os.Stdout, func() string { return prefix }))
	}

	mw := io.MultiWriter(writers...)
	cmd.Stdout = mw
	cmd.Stderr = mw

	if err := cmd.Start(); err != nil {
		if os.Getenv(kcpBinariesDirEnvDir) == "" && commandLine[0] == "kcp" {
			t.Log("Consider setting KCP_BINARIES_DIR pointing to a directory with a kcp binary.")
		}
		return nil, fmt.Errorf("failed to start kcp: %w", err)
	}

	go func() {
		<-ctx.Done()
		if cmd.Process != nil && cmd.Process.Pid > 0 {
			// Ensure child process is killed on cleanup - send the negative of the pid, which is the process group id.
			// See https://medium.com/@felixge/killing-a-child-process-and-all-of-its-children-in-go-54079af94773 for details.
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
				t.Errorf("Saw an error trying to kill `kcp`: %v", err)
			}
		}
	}()

	shutdownComplete := make(chan struct{})

	go func() {
		err := cmd.Wait()
		close(shutdownComplete)

		if err != nil && ctx.Err() == nil {
			// we care about errors in the process that did not result from the
			// context expiring and us ending the process
			data := filterKcpLogs(t, &log)
			t.Errorf("`kcp` failed: %v logs:\n%v", err, data)
			t.Errorf("`kcp` failed: %v", err)
		}
	}()

	return shutdownComplete, nil
}

// filterKcpLogs is a silly hack to get rid of the nonsense output that
// currently plagues kcp. Yes, in the future we want to actually fix these
// issues but until we do, there's no reason to force awful UX onto users.
func filterKcpLogs(t TestingT, logs *bytes.Buffer) string {
	output := strings.Builder{}
	scanner := bufio.NewScanner(logs)
	for scanner.Scan() {
		line := scanner.Bytes()
		ignored := false
		for _, ignore := range [][]byte{
			// TODO: some careful thought on context cancellation might fix the following error
			[]byte(`clientconn.go:1326] [core] grpc: addrConn.createTransport failed to connect to`),
		} {
			if bytes.Contains(line, ignore) {
				ignored = true
				continue
			}
		}
		if ignored {
			continue
		}
		_, err := output.Write(append(line, []byte("\n")...))
		if err != nil {
			t.Logf("failed to write log line: %v", err)
		}
	}
	return output.String()
}

// Name exposes the name of this kcp server.
func (c *kcpServer) Name() string {
	return c.cfg.Name
}

// KubeconfigPath exposes the path of the kubeconfig file of this kcp server.
func (c *kcpServer) KubeconfigPath() string {
	return filepath.Join(c.cfg.DataDir, "admin.kubeconfig")
}

// Config exposes a copy of the base client config for this server. Client-side throttling is disabled (QPS=-1).
func (c *kcpServer) config(context string) (*rest.Config, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.clientCfg == nil {
		return nil, fmt.Errorf("programmer error: kcpServer.Config() called before load succeeded. Stack: %s", string(debug.Stack()))
	}
	raw, err := c.clientCfg.RawConfig()
	if err != nil {
		return nil, err
	}

	config := clientcmd.NewNonInteractiveClientConfig(raw, context, nil, nil)

	restConfig, err := config.ClientConfig()
	if err != nil {
		return nil, err
	}

	restConfig.QPS = -1

	return restConfig, nil
}

func (c *kcpServer) ClientCAUserConfig(t TestingT, config *rest.Config, name string, groups ...string) *rest.Config {
	return clientCAUserConfig(t, config, c.cfg.ClientCADir, name, groups...)
}

// BaseConfig returns a rest.Config for the "base" context. Client-side throttling is disabled (QPS=-1).
func (c *kcpServer) BaseConfig(t TestingT) *rest.Config {
	t.Helper()

	cfg, err := c.config("base")
	require.NoError(t, err)
	cfg = rest.CopyConfig(cfg)
	return rest.AddUserAgent(cfg, t.Name())
}

// RootShardSystemMasterBaseConfig returns a rest.Config for the "shard-base" context. Client-side throttling is disabled (QPS=-1).
func (c *kcpServer) RootShardSystemMasterBaseConfig(t TestingT) *rest.Config {
	t.Helper()

	cfg, err := c.config("shard-base")
	require.NoError(t, err)
	cfg = rest.CopyConfig(cfg)

	return rest.AddUserAgent(cfg, t.Name())
}

// ShardSystemMasterBaseConfig returns a rest.Config for the "shard-base" context of a given shard. Client-side throttling is disabled (QPS=-1).
func (c *kcpServer) ShardSystemMasterBaseConfig(t TestingT, shard string) *rest.Config {
	t.Helper()

	if shard != corev1alpha1.RootShard {
		t.Fatalf("only root shard is supported for now")
	}
	return c.RootShardSystemMasterBaseConfig(t)
}

func (c *kcpServer) ShardNames() []string {
	return []string{corev1alpha1.RootShard}
}

// RawConfig exposes a copy of the client config for this server.
func (c *kcpServer) RawConfig() (clientcmdapi.Config, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.clientCfg == nil {
		return clientcmdapi.Config{}, fmt.Errorf("programmer error: kcpServer.RawConfig() called before load succeeded. Stack: %s", string(debug.Stack()))
	}
	return c.clientCfg.RawConfig()
}

func (c *kcpServer) loadCfg(ctx context.Context) error {
	var lastError error
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		if c.Stopped() {
			return false, fmt.Errorf("failed to load admin kubeconfig: server has stopped")
		}

		config, err := loadKubeConfig(c.KubeconfigPath(), "base")
		if err != nil {
			// A missing file is likely caused by the server not
			// having started up yet. Ignore these errors for the
			// purposes of logging.
			if !os.IsNotExist(err) {
				lastError = err
			}

			return false, nil
		}

		c.lock.Lock()
		c.clientCfg = config
		c.lock.Unlock()

		return true, nil
	}); err != nil && lastError != nil {
		return fmt.Errorf("failed to load admin kubeconfig: %w", lastError)
	} else if err != nil {
		// should never happen
		return fmt.Errorf("failed to load admin kubeconfig: %w", err)
	}
	return nil
}

func (c *kcpServer) CADirectory() string {
	return c.cfg.DataDir
}

func (c *kcpServer) Artifact(t TestingT, producer func() (runtime.Object, error)) {
	t.Helper()
	artifact(t, c, producer)
}

// artifact registers the data-producing function to run and dump the YAML-formatted output
// to the artifact directory for the test before the kcp process is terminated.
func artifact(t TestingT, server RunningServer, producer func() (runtime.Object, error)) {
	t.Helper()

	subDir := filepath.Join("artifacts", "kcp", server.Name())
	artifactDir, err := createTempDirForTest(t, subDir)
	require.NoError(t, err, "could not create artifacts dir")
	// Using t.Cleanup ensures that artifact collection is local to
	// the test requesting retention regardless of server's scope.
	t.Cleanup(func() {
		data, err := producer()
		require.NoError(t, err, "error fetching artifact")

		accessor, ok := data.(metav1.Object)
		require.True(t, ok, "artifact has no object meta: %#v", data)

		dir := path.Join(artifactDir, logicalcluster.From(accessor).String())
		dir = strings.ReplaceAll(dir, ":", "_") // github actions don't like colon because NTFS is unhappy with it in path names
		if accessor.GetNamespace() != "" {
			dir = path.Join(dir, accessor.GetNamespace())
		}
		err = os.MkdirAll(dir, 0755)
		require.NoError(t, err, "could not create dir")

		gvks, _, err := kubernetesscheme.Scheme.ObjectKinds(data)
		if err != nil {
			gvks, _, err = kcpscheme.Scheme.ObjectKinds(data)
		}
		require.NoError(t, err, "error finding gvk for artifact")
		require.NotEmpty(t, gvks, "found no gvk for artifact: %T", data)
		gvk := gvks[0]
		data.GetObjectKind().SetGroupVersionKind(gvk)

		group := gvk.Group
		if group == "" {
			group = "core"
		}

		gvkForFilename := fmt.Sprintf("%s_%s", group, gvk.Kind)

		file := path.Join(dir, fmt.Sprintf("%s-%s.yaml", gvkForFilename, accessor.GetName()))
		file = strings.ReplaceAll(file, ":", "_") // github actions don't like colon because NTFS is unhappy with it in path names

		bs, err := yaml.Marshal(data)
		require.NoError(t, err, "error marshalling artifact")

		err = os.WriteFile(file, bs, 0644)
		require.NoError(t, err, "error writing artifact")
	})
}
