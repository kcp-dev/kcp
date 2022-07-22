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

package framework

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/egymgmbh/go-prefix-writer/prefixer"
	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"

	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/kcp/pkg/server"
	"github.com/kcp-dev/kcp/pkg/server/options"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
)

type RunningServer interface {
	Name() string
	KubeconfigPath() string
	RawConfig() (clientcmdapi.Config, error)
	BaseConfig(t *testing.T) *rest.Config
	RootShardSystemMasterBaseConfig(t *testing.T) *rest.Config
	Artifact(t *testing.T, producer func() (runtime.Object, error))
}

// kcpConfig qualify a kcp server to start
//
// Deprecated for use outside this package. Prefer PrivateKcpServer().
type kcpConfig struct {
	Name string
	Args []string

	LogToConsole bool
	RunInProcess bool
}

// kcpServer exposes a kcp invocation to a test and
// ensures the following semantics:
//  - the server will run only until the test deadline
//  - all ports and data directories are unique to support
//    concurrent execution within a test case and across tests
type kcpServer struct {
	name        string
	args        []string
	ctx         context.Context
	dataDir     string
	artifactDir string

	lock           *sync.Mutex
	cfg            clientcmd.ClientConfig
	kubeconfigPath string

	t *testing.T
}

func newKcpServer(t *testing.T, cfg kcpConfig, artifactDir, dataDir string) (*kcpServer, error) {
	t.Helper()

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
	artifactDir = filepath.Join(artifactDir, "kcp", cfg.Name)
	if err := os.MkdirAll(artifactDir, 0755); err != nil {
		return nil, fmt.Errorf("could not create artifact dir: %w", err)
	}
	dataDir = filepath.Join(dataDir, "kcp", cfg.Name)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("could not create data dir: %w", err)
	}

	return &kcpServer{
		name: cfg.Name,
		args: append([]string{
			"--root-directory",
			dataDir,
			"--secure-port=" + kcpListenPort,
			"--embedded-etcd-client-port=" + etcdClientPort,
			"--embedded-etcd-peer-port=" + etcdPeerPort,
			"--embedded-etcd-wal-size-bytes=" + strconv.Itoa(5*1000), // 5KB
			"--kubeconfig-path=" + filepath.Join(dataDir, "admin.kubeconfig"),
		},
			cfg.Args...),
		dataDir:     dataDir,
		artifactDir: artifactDir,
		t:           t,
		lock:        &sync.Mutex{},
	}, nil
}

type runOptions struct {
	runInProcess bool
	streamLogs   bool
}

type RunOption func(o *runOptions)

func RunInProcess(o *runOptions) {
	o.runInProcess = true
}

func WithLogStreaming(o *runOptions) {
	o.streamLogs = true
}

// RepositoryDir returns the absolute path of <repo-dir>.
func RepositoryDir() string {
	// Caller(0) returns the path to the calling test file rather than the path to this framework file. That
	// precludes assuming how many directories are between the file and the repo root. It's therefore necessary
	// to search in the hierarchy for an indication of a path that looks like the repo root.
	_, sourceFile, _, _ := goruntime.Caller(0)
	currentDir := filepath.Dir(sourceFile)
	for {
		// go.mod should always exist in the repo root
		if _, err := os.Stat(filepath.Join(currentDir, "go.mod")); err == nil {
			break
		} else if errors.Is(err, os.ErrNotExist) {
			currentDir, err = filepath.Abs(filepath.Join(currentDir, ".."))
			if err != nil {
				panic(err)
			}
		} else {
			panic(err)
		}
	}
	return currentDir
}

// RepositoryBinDir returns the absolute path of <repo-dir>/bin. That's where `make build` produces our binaries.
func RepositoryBinDir() string {
	return filepath.Join(RepositoryDir(), "bin")
}

// StartKcpCommand returns the string tokens required to start kcp in
// the currently configured mode (direct or via `go run`).
func StartKcpCommand() []string {
	command := DirectOrGoRunCommand("kcp")
	return append(command, "start")
}

// DirectOrGoRunCommand returns the string tokens required to start
// the given executable in the currently configured mode (direct or
// via `go run`).
func DirectOrGoRunCommand(executableName string) []string {
	if NoGoRunEnvSet() {
		cmdPath := filepath.Join(RepositoryBinDir(), executableName)
		return []string{cmdPath}
	} else {
		cmdPath := filepath.Join(RepositoryDir(), "cmd", executableName)
		return []string{"go", "run", cmdPath}
	}
}

// Run runs the kcp server while the parent context is active. This call is not blocking,
// callers should ensure that the server is Ready() before using it.
func (c *kcpServer) Run(opts ...RunOption) error {
	runOpts := runOptions{}
	for _, opt := range opts {
		opt(&runOpts)
	}

	// We close this channel when the kcp server has stopped
	shutdownComplete := make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())

	cleanup := func() {
		cancel()
		close(shutdownComplete)
	}

	c.t.Cleanup(func() {
		c.t.Log("cleanup: canceling context")
		cancel()

		// Wait for the kcp server to stop
		c.t.Log("cleanup: waiting for shutdownComplete")

		<-shutdownComplete

		c.t.Log("cleanup: received shutdownComplete")
	})
	c.ctx = ctx

	commandLine := append(StartKcpCommand(), c.args...)
	c.t.Logf("running: %v", strings.Join(commandLine, " "))

	// run kcp start in-process for easier debugging
	if runOpts.runInProcess {
		rootDir := ".kcp"
		if c.dataDir != "" {
			rootDir = c.dataDir
		}
		serverOptions := options.NewOptions(rootDir)
		all := pflag.NewFlagSet("kcp", pflag.ContinueOnError)
		for _, fs := range serverOptions.Flags().FlagSets {
			all.AddFlagSet(fs)
		}
		if err := all.Parse(c.args); err != nil {
			cleanup()
			return err
		}

		completed, err := serverOptions.Complete()
		if err != nil {
			cleanup()
			return err
		}
		if errs := completed.Validate(); len(errs) > 0 {
			cleanup()
			return apierrors.NewAggregate(errs)
		}

		s, err := server.NewServer(completed)
		if err != nil {
			cleanup()
			return err
		}
		go func() {
			defer cleanup()

			if err := s.Run(ctx); err != nil && ctx.Err() == nil {
				c.t.Errorf("`kcp` failed: %v", err)
			}
		}()

		return nil
	}

	// NOTE: do not use exec.CommandContext here. That method issues a SIGKILL when the context is done, and we
	// want to issue SIGTERM instead, to give the server a chance to shut down cleanly.
	cmd := exec.Command(commandLine[0], commandLine[1:]...)
	logFile, err := os.Create(filepath.Join(c.artifactDir, "kcp.log"))
	if err != nil {
		cleanup()
		return fmt.Errorf("could not create log file: %w", err)
	}

	// Closing the logfile is necessary so the cmd.Wait() call in the goroutine below can finish (it only finishes
	// waiting when the internal io.Copy goroutines for stdin/stdout/stderr are done, and that doesn't happen if
	// the log file remains open.
	c.t.Cleanup(func() {
		logFile.Close()
	})

	log := bytes.Buffer{}

	writers := []io.Writer{&log, logFile}

	if runOpts.streamLogs {
		prefix := fmt.Sprintf("%s: ", c.name)
		writers = append(writers, prefixer.New(os.Stdout, func() string { return prefix }))
	}

	mw := io.MultiWriter(writers...)
	cmd.Stdout = mw
	cmd.Stderr = mw

	if err := cmd.Start(); err != nil {
		cleanup()
		return err
	}

	c.t.Cleanup(func() {
		// Ensure child process is killed on cleanup
		err := cmd.Process.Signal(syscall.SIGTERM)
		if err != nil {
			c.t.Errorf("Saw an error trying to kill `kcp`: %v", err)
		}
	})

	go func() {
		defer cleanup()

		err := cmd.Wait()

		if err != nil && ctx.Err() == nil {
			// we care about errors in the process that did not result from the
			// context expiring and us ending the process
			data := c.filterKcpLogs(&log)
			c.t.Errorf("`kcp` failed: %v logs:\n%v", err, data)
		}
	}()

	return nil
}

// filterKcpLogs is a silly hack to get rid of the nonsense output that
// currently plagues kcp. Yes, in the future we want to actually fix these
// issues but until we do, there's no reason to force awful UX onto users.
func (c *kcpServer) filterKcpLogs(logs *bytes.Buffer) string {
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
			c.t.Logf("failed to write log line: %v", err)
		}
	}
	return output.String()
}

// Name exposes the name of this kcp server
func (c *kcpServer) Name() string {
	return c.name
}

// Name exposes the path of the kubeconfig file of this kcp server
func (c *kcpServer) KubeconfigPath() string {
	return c.kubeconfigPath
}

// Config exposes a copy of the base client config for this server. Client-side throttling is disabled (QPS=-1).
func (c *kcpServer) config(context string) (*rest.Config, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.cfg == nil {
		return nil, fmt.Errorf("programmer error: kcpServer.Config() called before load succeeded. Stack: %s", string(debug.Stack()))
	}
	raw, err := c.cfg.RawConfig()
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

// BaseConfig returns a rest.Config for the "base" context. Client-side throttling is disabled (QPS=-1).
func (c *kcpServer) BaseConfig(t *testing.T) *rest.Config {
	cfg, err := c.config("base")
	require.NoError(t, err)
	cfg = kcpclienthelper.NewClusterConfig(cfg)
	return rest.AddUserAgent(rest.CopyConfig(cfg), t.Name())
}

// RootShardSystemMasterBaseConfig returns a rest.Config for the "system:admin" context. Client-side throttling is disabled (QPS=-1).
func (c *kcpServer) RootShardSystemMasterBaseConfig(t *testing.T) *rest.Config {
	cfg, err := c.config("system:admin")
	require.NoError(t, err)
	cfg = kcpclienthelper.NewClusterConfig(cfg)
	return rest.AddUserAgent(rest.CopyConfig(cfg), t.Name())
}

// RawConfig exposes a copy of the client config for this server.
func (c *kcpServer) RawConfig() (clientcmdapi.Config, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.cfg == nil {
		return clientcmdapi.Config{}, fmt.Errorf("programmer error: kcpServer.RawConfig() called before load succeeded. Stack: %s", string(debug.Stack()))
	}
	return c.cfg.RawConfig()
}

// Ready blocks until the server is healthy and ready. Before returning,
// goroutines are started to ensure that the test is failed if the server
// does not remain so.
func (c *kcpServer) Ready(keepMonitoring bool) error {
	if err := c.loadCfg(); err != nil {
		return err
	}
	if c.ctx.Err() != nil {
		// cancelling the context will preempt derivative calls but not this
		// main Ready() body, so we check before continuing that we are live
		return fmt.Errorf("failed to wait for readiness: %w", c.ctx.Err())
	}
	cfg, err := c.config("base")
	if err != nil {
		return fmt.Errorf("failed to read client configuration: %w", err)
	}
	if cfg.NegotiatedSerializer == nil {
		cfg.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	}
	client, err := rest.UnversionedRESTClientFor(cfg)
	if err != nil {
		return fmt.Errorf("failed to create unversioned client: %w", err)
	}

	wg := sync.WaitGroup{}
	wg.Add(2)
	for _, endpoint := range []string{"/livez", "/readyz"} {
		go func(endpoint string) {
			defer wg.Done()
			c.waitForEndpoint(client, endpoint)
		}(endpoint)
	}
	wg.Wait()

	if keepMonitoring {
		for _, endpoint := range []string{"/livez", "/readyz"} {
			go func(endpoint string) {
				c.monitorEndpoint(client, endpoint)
			}(endpoint)
		}
	}
	return nil
}

func (c *kcpServer) loadCfg() error {
	var lastError error
	if err := wait.PollImmediateWithContext(c.ctx, 100*time.Millisecond, 1*time.Minute, func(ctx context.Context) (bool, error) {
		c.kubeconfigPath = filepath.Join(c.dataDir, "admin.kubeconfig")
		config, err := loadKubeConfig(c.kubeconfigPath)
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
		c.cfg = config
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

func (c *kcpServer) waitForEndpoint(client *rest.RESTClient, endpoint string) {
	var lastError error
	if err := wait.PollImmediateWithContext(c.ctx, 100*time.Millisecond, time.Minute, func(ctx context.Context) (bool, error) {
		req := rest.NewRequest(client).RequestURI(endpoint)
		_, err := req.Do(ctx).Raw()
		if err != nil {
			lastError = fmt.Errorf("error contacting %s: failed components: %v", req.URL(), unreadyComponentsFromError(err))
			return false, nil
		}

		c.t.Logf("success contacting %s", req.URL())
		return true, nil
	}); err != nil && lastError != nil {
		c.t.Error(lastError)
	}
}

func (c *kcpServer) monitorEndpoint(client *rest.RESTClient, endpoint string) {
	// we need a shorter deadline than the server, or else:
	// timeout.go:135] post-timeout activity - time-elapsed: 23.784917ms, GET "/livez" result: Header called after Handler finished
	ctx := c.ctx
	if deadline, ok := c.t.Deadline(); ok {
		deadlinedCtx, deadlinedCancel := context.WithDeadline(c.ctx, deadline.Add(-20*time.Second))
		ctx = deadlinedCtx
		c.t.Cleanup(deadlinedCancel) // this does not really matter but govet is upset
	}
	var errCount int
	errs := sets.NewString()
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		_, err := rest.NewRequest(client).RequestURI(endpoint).Do(ctx).Raw()
		if errors.Is(err, context.Canceled) || c.ctx.Err() != nil {
			return
		}
		// if we're noticing an error, record it and fail the test if things stay failed for two consecutive polls
		if err != nil {
			errCount++
			errs.Insert(fmt.Sprintf("failed components: %v", unreadyComponentsFromError(err)))
			if errCount == 2 {
				c.t.Errorf("error contacting %s: %v", endpoint, errs.List())
			}
		}
		// otherwise, reset the counters
		errCount = 0
		if errs.Len() > 0 {
			errs = sets.NewString()
		}
	}, 1*time.Second)
}

// there doesn't seem to be any simple way to get a metav1.Status from the Go client, so we get
// the content in a string-formatted error, unfortunately.
func unreadyComponentsFromError(err error) string {
	innerErr := strings.TrimPrefix(strings.TrimSuffix(err.Error(), `") has prevented the request from succeeding`), `an error on the server ("`)
	var unreadyComponents []string
	for _, line := range strings.Split(innerErr, `\n`) {
		if name := strings.TrimPrefix(strings.TrimSuffix(line, ` failed: reason withheld`), `[-]`); name != line {
			// NB: sometimes the error we get is truncated (server-side?) to something like: `\n[-]poststar") has prevented the request from succeeding`
			// In those cases, the `name` here is also truncated, but nothing we can do about that. For that reason, we don't expose a list of components
			// from this function or else we'd need to handle more edge cases.
			unreadyComponents = append(unreadyComponents, name)
		}
	}
	return strings.Join(unreadyComponents, ", ")
}

// loadKubeConfig loads a kubeconfig from disk. This method is
// intended to be common between fixture for servers whose lifecycle
// is test-managed and fixture for servers whose lifecycle is managed
// separately from a test run.
func loadKubeConfig(kubeconfigPath string) (clientcmd.ClientConfig, error) {
	fs, err := os.Stat(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	if fs.Size() == 0 {
		return nil, fmt.Errorf("%s points to an empty file", kubeconfigPath)
	}

	rawConfig, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load admin kubeconfig: %w", err)
	}

	return clientcmd.NewNonInteractiveClientConfig(*rawConfig, "base", nil, nil), nil
}

type unmanagedKCPServer struct {
	name                    string
	kubeconfigPath          string
	rootShardKubeconfigPath string
	cfg, rootShardCfg       clientcmd.ClientConfig
}

// newPersistentKCPServer returns a RunningServer for a kubeconfig
// pointing to a kcp instance not managed by the test run. Since the
// kubeconfig is expected to exist prior to running tests against it,
// the configuration can be loaded synchronously and no locking is
// required to subsequently access it.
func newPersistentKCPServer(name, kubeconfigPath, rootShardKubeconfigPath string) (RunningServer, error) {
	cfg, err := loadKubeConfig(kubeconfigPath)
	if err != nil {
		return nil, err
	}

	rootShardCfg, err := loadKubeConfig(rootShardKubeconfigPath)
	if err != nil {
		return nil, err
	}

	return &unmanagedKCPServer{
		name:                    name,
		kubeconfigPath:          kubeconfigPath,
		rootShardKubeconfigPath: rootShardKubeconfigPath,
		cfg:                     cfg,
		rootShardCfg:            rootShardCfg,
	}, nil
}

// NewFakeWorkloadServer creates a workspace in the provided server and org
// and creates a server fixture for the logical cluster that results.
func NewFakeWorkloadServer(t *testing.T, server RunningServer, org logicalcluster.Name) RunningServer {
	logicalClusterName := NewWorkspaceFixture(t, server, org)
	rawConfig, err := server.RawConfig()
	require.NoError(t, err, "failed to read config for server")
	logicalConfig, kubeconfigPath := WriteLogicalClusterConfig(t, rawConfig, logicalClusterName, "base")
	fakeServer := &unmanagedKCPServer{
		name:           logicalClusterName.String(),
		cfg:            logicalConfig,
		kubeconfigPath: kubeconfigPath,
	}

	downstreamConfig := fakeServer.BaseConfig(t)

	// Install the deployment crd in the fake cluster to allow creation of the syncer deployment.
	crdClient, err := apiextensionsclientset.NewForConfig(downstreamConfig)
	require.NoError(t, err)
	kubefixtures.Create(t, crdClient.ApiextensionsV1().CustomResourceDefinitions(),
		metav1.GroupResource{Group: "apps.k8s.io", Resource: "deployments"},
	)

	// Wait for the deployment crd to become ready
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)
	kubeClient, err := kubernetesclientset.NewForConfig(downstreamConfig)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, err := kubeClient.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Logf("error seen waiting for deployment crd to become active: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	return fakeServer
}

func (s *unmanagedKCPServer) Name() string {
	return s.name
}

func (s *unmanagedKCPServer) KubeconfigPath() string {
	return s.kubeconfigPath
}

func (s *unmanagedKCPServer) RawConfig() (clientcmdapi.Config, error) {
	return s.cfg.RawConfig()
}

// BaseConfig returns a rest.Config for the "base" context. Client-side throttling is disabled (QPS=-1).
func (s *unmanagedKCPServer) BaseConfig(t *testing.T) *rest.Config {
	raw, err := s.cfg.RawConfig()
	require.NoError(t, err)

	config := clientcmd.NewNonInteractiveClientConfig(raw, "base", nil, nil)

	defaultConfig, err := config.ClientConfig()
	require.NoError(t, err)

	wrappedCfg := kcpclienthelper.NewClusterConfig(defaultConfig)
	wrappedCfg.QPS = -1

	return wrappedCfg
}

// RootShardSystemMasterBaseConfig returns a rest.Config for the "system:admin" context. Client-side throttling is disabled (QPS=-1).
func (s *unmanagedKCPServer) RootShardSystemMasterBaseConfig(t *testing.T) *rest.Config {
	raw, err := s.rootShardCfg.RawConfig()
	require.NoError(t, err)

	config := clientcmd.NewNonInteractiveClientConfig(raw, "system:admin", nil, nil)

	defaultConfig, err := config.ClientConfig()
	require.NoError(t, err)

	defaultConfig.QPS = -1

	return defaultConfig
}

func (s *unmanagedKCPServer) Artifact(t *testing.T, producer func() (runtime.Object, error)) {
	artifact(t, s, producer)
}

func NoGoRunEnvSet() bool {
	envSet, _ := strconv.ParseBool(os.Getenv("NO_GORUN"))
	return envSet
}
