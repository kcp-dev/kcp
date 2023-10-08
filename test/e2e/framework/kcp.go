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
	"net/http"
	"net/url"
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
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
	gopkgyaml "gopkg.in/yaml.v3"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apierrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/kubernetes"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/component-base/cli/flag"

	kcpoptions "github.com/kcp-dev/kcp/cmd/kcp/options"
	"github.com/kcp-dev/kcp/cmd/sharded-test-server/third_party/library-go/crypto"
	"github.com/kcp-dev/kcp/pkg/embeddedetcd"
	"github.com/kcp-dev/kcp/pkg/server"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
)

// TestServerArgs returns the set of kcp args used to start a test
// server using the token auth file from the working tree.
func TestServerArgs() []string {
	return append(
		TestServerArgsWithTokenAuthFile("test/e2e/framework/auth-tokens.csv"),
		TestServerWithAuditPolicyFile("test/e2e/framework/audit-policy.yaml")...,
	)
}

// TestServerWithAuditPolicyFile returns the set of kcp args used to
// start a test server with the given audit policy file.
func TestServerWithAuditPolicyFile(auditPolicyFile string) []string {
	return []string{
		"--audit-policy-file", auditPolicyFile,
	}
}

func TestServerWithClientCAFile(clientCAFile string) []string {
	return []string{
		"--client-ca-file", clientCAFile,
	}
}

// TestServerArgsWithTokenAuthFile returns the set of kcp args used to
// start a test server with the given token auth file.
func TestServerArgsWithTokenAuthFile(tokenAuthFile string) []string {
	return []string{
		"-v=4",
		"--token-auth-file", tokenAuthFile,
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
func PrivateKcpServer(t *testing.T, options ...KcpConfigOption) RunningServer {
	t.Helper()

	serverName := "main"

	cfg := &kcpConfig{Name: serverName}
	for _, opt := range options {
		cfg = opt(cfg)
	}

	auditPolicyArg := false
	for _, arg := range cfg.Args {
		if arg == "--audit-policy-file" {
			auditPolicyArg = true
		}
	}
	// Default --audit-policy-file or we get no audit info for CI debugging
	if !auditPolicyArg {
		cfg.Args = append(cfg.Args, TestServerWithAuditPolicyFile(WriteEmbedFile(t, "audit-policy.yaml"))...)
	}

	if len(cfg.ArtifactDir) == 0 || len(cfg.DataDir) == 0 {
		artifactDir, dataDir, err := ScratchDirs(t)
		require.NoError(t, err, "failed to create scratch dirs: %v", err)
		cfg.ArtifactDir = artifactDir
		cfg.DataDir = dataDir
	}

	f := newKcpFixture(t, *cfg)
	return f.Servers[serverName]
}

// SharedKcpServer returns a kcp server fixture intended to be shared
// between tests. A persistent server will be configured if
// `--kcp-kubeconfig` or `--use-default-kcp-server` is supplied to the test
// runner. Otherwise a test-managed server will be started. Only tests
// that are known to be hermetic are compatible with shared fixture.
func SharedKcpServer(t *testing.T) RunningServer {
	t.Helper()

	serverName := "shared"
	kubeconfig := TestConfig.KCPKubeconfig()
	if len(kubeconfig) > 0 {
		// Use a persistent server

		t.Logf("shared kcp server will target configuration %q", kubeconfig)
		server, err := newPersistentKCPServer(serverName, kubeconfig, TestConfig.ShardKubeconfig(), filepath.Join(RepositoryDir(), ".kcp"))
		require.NoError(t, err, "failed to create persistent server fixture")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		t.Cleanup(cancel)
		err = WaitForReady(ctx, t, server.RootShardSystemMasterBaseConfig(t), true)
		require.NoError(t, err, "error waiting for readiness")

		return server
	}

	// Use a test-provisioned server
	//
	// TODO(marun) Enable non-persistent fixture to be shared across
	// tests. This will likely require composing tests into a suite that
	// initializes the shared fixture before tests that rely on the
	// fixture.

	artifactDir, dataDir, err := ScratchDirs(t)
	require.NoError(t, err, "failed to create scratch dirs: %v", err)

	args := TestServerArgsWithTokenAuthFile(WriteTokenAuthFile(t))
	args = append(args, TestServerWithAuditPolicyFile(WriteEmbedFile(t, "audit-policy.yaml"))...)
	clientCADir, clientCAFile := CreateClientCA(t)
	args = append(args, TestServerWithClientCAFile(clientCAFile)...)
	f := newKcpFixture(t, kcpConfig{
		Name:        serverName,
		Args:        args,
		ArtifactDir: artifactDir,
		ClientCADir: clientCADir,
		DataDir:     dataDir,
	})
	return f.Servers[serverName]
}

func GatherMetrics(ctx context.Context, t *testing.T, server RunningServer, directory string) {
	cfg := server.RootShardSystemMasterBaseConfig(t)
	client, err := kcpclientset.NewForConfig(cfg)
	if err != nil {
		// Don't fail the test if we couldn't scrape metrics
		t.Logf("error creating metrics client for server %s: %v", server.Name(), err)
	}

	raw, err := client.RESTClient().Get().RequestURI("/metrics").DoRaw(ctx)
	if err != nil {
		// Don't fail the test if we couldn't scrape metrics
		t.Logf("error getting metrics for server %s: %v", server.Name(), err)
		return
	}

	metricsFile := filepath.Join(directory, fmt.Sprintf("%s-metrics.txt", server.Name()))
	if err := os.WriteFile(metricsFile, raw, 0o644); err != nil {
		// Don't fail the test if we couldn't scrape metrics
		t.Logf("error writing metrics file %s: %v", metricsFile, err)
	}
}

func ScrapeMetricsForServer(t *testing.T, srv RunningServer) {
	promUrl, set := os.LookupEnv("PROMETHEUS_URL")
	if !set || promUrl == "" {
		t.Logf("PROMETHEUS_URL environment variable unset, skipping Prometheus scrape config generation")
		return
	}
	jobName := fmt.Sprintf("kcp-%s-%s", srv.Name(), t.Name())
	labels := map[string]string{
		"server": srv.Name(),
		"test":   t.Name(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), wait.ForeverTestTimeout)
	defer cancel()
	require.NoError(t, ScrapeMetrics(ctx, srv.RootShardSystemMasterBaseConfig(t), promUrl, RepositoryDir(), jobName, filepath.Join(srv.CADirectory(), "apiserver.crt"), labels))
}

func ScrapeMetrics(ctx context.Context, cfg *rest.Config, promUrl, promCfgDir, jobName, caFile string, labels map[string]string) error {
	jobName = fmt.Sprintf("%s-%d", jobName, time.Now().Unix())
	type staticConfigs struct {
		Targets []string          `yaml:"targets,omitempty"`
		Labels  map[string]string `yaml:"labels,omitempty"`
	}
	type tlsConfig struct {
		InsecureSkipVerify bool   `yaml:"insecure_skip_verify,omitempty"`
		CaFile             string `yaml:"ca_file,omitempty"`
	}
	type scrapeConfig struct {
		JobName        string          `yaml:"job_name,omitempty"`
		ScrapeInterval string          `yaml:"scrape_interval,omitempty"`
		BearerToken    string          `yaml:"bearer_token,omitempty"`
		TlsConfig      tlsConfig       `yaml:"tls_config,omitempty"`
		Scheme         string          `yaml:"scheme,omitempty"`
		StaticConfigs  []staticConfigs `yaml:"static_configs,omitempty"`
	}
	type config struct {
		ScrapeConfigs []scrapeConfig `yaml:"scrape_configs,omitempty"`
	}
	err := func() error {
		scrapeConfigFile := filepath.Join(promCfgDir, ".prometheus-config.yaml")
		f, err := os.OpenFile(scrapeConfigFile, os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		// lock config file exclusively, blocks all other producers until unlocked or process (test) exits
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != nil {
			return err
		}
		promCfg := config{}
		err = gopkgyaml.NewDecoder(f).Decode(&promCfg)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		hostUrl, err := url.Parse(cfg.Host)
		if err != nil {
			return err
		}
		promCfg.ScrapeConfigs = append(promCfg.ScrapeConfigs, scrapeConfig{
			JobName:        jobName,
			ScrapeInterval: (5 * time.Second).String(),
			BearerToken:    cfg.BearerToken,
			TlsConfig:      tlsConfig{CaFile: caFile},
			Scheme:         hostUrl.Scheme,
			StaticConfigs: []staticConfigs{{
				Targets: []string{hostUrl.Host},
				Labels:  labels,
			}},
		})
		err = f.Truncate(0)
		if err != nil {
			return err
		}
		_, err = f.Seek(0, 0)
		if err != nil {
			return err
		}
		err = gopkgyaml.NewEncoder(f).Encode(&promCfg)
		if err != nil {
			return err
		}
		return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}()
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, promUrl+"/-/reload", http.NoBody)
	if err != nil {
		return err
	}
	c := &http.Client{}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func CreateClientCA(t *testing.T) (string, string) {
	clientCADir := t.TempDir()
	_, err := crypto.MakeSelfSignedCA(
		filepath.Join(clientCADir, "client-ca.crt"),
		filepath.Join(clientCADir, "client-ca.key"),
		filepath.Join(clientCADir, "client-ca-serial.txt"),
		"kcp-client-ca",
		365,
	)
	require.NoError(t, err)
	return clientCADir, filepath.Join(clientCADir, "client-ca.crt")
}

// Deprecated for use outside this package. Prefer PrivateKcpServer().
func newKcpFixture(t *testing.T, cfgs ...kcpConfig) *kcpFixture {
	t.Helper()

	f := &kcpFixture{}

	// Initialize servers from the provided configuration
	servers := make([]*kcpServer, 0, len(cfgs))
	f.Servers = make(map[string]RunningServer, len(cfgs))
	for _, cfg := range cfgs {
		if len(cfg.ArtifactDir) == 0 {
			panic(fmt.Sprintf("provided kcpConfig for %s is incorrect, missing ArtifactDir", cfg.Name))
		}
		if len(cfg.DataDir) == 0 {
			panic(fmt.Sprintf("provided kcpConfig for %s is incorrect, missing DataDir", cfg.Name))
		}
		server, err := newKcpServer(t, cfg, cfg.ArtifactDir, cfg.DataDir, cfg.ClientCADir)
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

			err := s.loadCfg()
			require.NoError(t, err, "error loading config")

			err = WaitForReady(s.ctx, t, s.RootShardSystemMasterBaseConfig(t), !cfgs[i].RunInProcess)
			require.NoError(t, err, "kcp server %s never became ready: %v", s.name, err)
		}(srv, i)
	}
	wg.Wait()

	for _, server := range servers {
		ScrapeMetricsForServer(t, server)
	}

	if t.Failed() {
		t.Fatal("Fixture setup failed: one or more servers did not become ready")
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), wait.ForeverTestTimeout)
		defer cancel()

		for _, server := range servers {
			GatherMetrics(ctx, t, server, server.artifactDir)
		}
	})

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

func preserveTestResources() bool {
	return os.Getenv("PRESERVE") != ""
}

type RunningServer interface {
	Name() string
	KubeconfigPath() string
	RawConfig() (clientcmdapi.Config, error)
	BaseConfig(t *testing.T) *rest.Config
	RootShardSystemMasterBaseConfig(t *testing.T) *rest.Config
	ShardSystemMasterBaseConfig(t *testing.T, shard string) *rest.Config
	ShardNames() []string
	Artifact(t *testing.T, producer func() (runtime.Object, error))
	ClientCAUserConfig(t *testing.T, config *rest.Config, name string, groups ...string) *rest.Config
	CADirectory() string
}

// KcpConfigOption a function that wish to modify a given kcp configuration.
type KcpConfigOption func(*kcpConfig) *kcpConfig

// WithScratchDirectories adds custom scratch directories to a kcp configuration.
func WithScratchDirectories(artifactDir, dataDir string) KcpConfigOption {
	return func(cfg *kcpConfig) *kcpConfig {
		cfg.ArtifactDir = artifactDir
		cfg.DataDir = dataDir
		return cfg
	}
}

// WithCustomArguments applies provided arguments to a given kcp configuration.
func WithCustomArguments(args ...string) KcpConfigOption {
	return func(cfg *kcpConfig) *kcpConfig {
		cfg.Args = args
		return cfg
	}
}

// kcpConfig qualify a kcp server to start
//
// Deprecated for use outside this package. Prefer PrivateKcpServer().
type kcpConfig struct {
	Name        string
	Args        []string
	ArtifactDir string
	DataDir     string
	ClientCADir string

	LogToConsole bool
	RunInProcess bool
}

// kcpServer exposes a kcp invocation to a test and
// ensures the following semantics:
//   - the server will run only until the test deadline
//   - all ports and data directories are unique to support
//     concurrent execution within a test case and across tests
type kcpServer struct {
	name        string
	args        []string
	ctx         context.Context //nolint:containedctx
	dataDir     string
	artifactDir string
	clientCADir string

	lock           *sync.Mutex
	cfg            clientcmd.ClientConfig
	kubeconfigPath string

	t *testing.T
}

func newKcpServer(t *testing.T, cfg kcpConfig, artifactDir, dataDir, clientCADir string) (*kcpServer, error) {
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
			"--feature-gates=" + fmt.Sprintf("%s", utilfeature.DefaultFeatureGate),
			"--audit-log-path", filepath.Join(artifactDir, "kcp.audit"),
		},
			cfg.Args...),
		dataDir:     dataDir,
		artifactDir: artifactDir,
		clientCADir: clientCADir,
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
	}
	cmdPath := filepath.Join(RepositoryDir(), "cmd", executableName)
	return []string{"go", "run", cmdPath}
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
		serverOptions := kcpoptions.NewOptions(rootDir)
		fss := flag.NamedFlagSets{}
		serverOptions.AddFlags(&fss)
		all := pflag.NewFlagSet("kcp", pflag.ContinueOnError)
		for _, fs := range fss.FlagSets {
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

		config, err := server.NewConfig(completed.Server)
		if err != nil {
			cleanup()
			return err
		}

		completedConfig, err := config.Complete()
		if err != nil {
			cleanup()
			return err
		}

		// the etcd server must be up before NewServer because storage decorators access it right away
		if completedConfig.EmbeddedEtcd.Config != nil {
			if err := embeddedetcd.NewServer(completedConfig.EmbeddedEtcd).Run(ctx); err != nil {
				return err
			}
		}

		s, err := server.NewServer(completedConfig)
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

	// Create a new process group for the child/forked process (which is either 'go run ...' or just 'kcp
	// ...'). This is necessary so the SIGTERM we send to terminate the kcp server works even with the
	// 'go run' variant - we have to work around this issue: https://github.com/golang/go/issues/40467.
	// Thanks to
	// https://medium.com/@felixge/killing-a-child-process-and-all-of-its-children-in-go-54079af94773 for
	// the idea!
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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
		// Ensure child process is killed on cleanup - send the negative of the pid, which is the process group id.
		// See https://medium.com/@felixge/killing-a-child-process-and-all-of-its-children-in-go-54079af94773 for details.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
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
			c.t.Errorf("`kcp` failed: %v", err)
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

// Name exposes the name of this kcp server.
func (c *kcpServer) Name() string {
	return c.name
}

// Name exposes the path of the kubeconfig file of this kcp server.
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

func (c *kcpServer) ClientCAUserConfig(t *testing.T, config *rest.Config, name string, groups ...string) *rest.Config {
	return ClientCAUserConfig(t, config, c.clientCADir, name, groups...)
}

// BaseConfig returns a rest.Config for the "base" context. Client-side throttling is disabled (QPS=-1).
func (c *kcpServer) BaseConfig(t *testing.T) *rest.Config {
	t.Helper()

	cfg, err := c.config("base")
	require.NoError(t, err)
	cfg = rest.CopyConfig(cfg)
	return rest.AddUserAgent(cfg, t.Name())
}

// RootShardSystemMasterBaseConfig returns a rest.Config for the "system:admin" context. Client-side throttling is disabled (QPS=-1).
func (c *kcpServer) RootShardSystemMasterBaseConfig(t *testing.T) *rest.Config {
	t.Helper()

	cfg, err := c.config("system:admin")
	require.NoError(t, err)
	cfg = rest.CopyConfig(cfg)
	return rest.AddUserAgent(cfg, t.Name())
}

// ShardSystemMasterBaseConfig returns a rest.Config for the "system:admin" context of a given shard. Client-side throttling is disabled (QPS=-1).
func (c *kcpServer) ShardSystemMasterBaseConfig(t *testing.T, shard string) *rest.Config {
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
	if c.cfg == nil {
		return clientcmdapi.Config{}, fmt.Errorf("programmer error: kcpServer.RawConfig() called before load succeeded. Stack: %s", string(debug.Stack()))
	}
	return c.cfg.RawConfig()
}

func (c *kcpServer) loadCfg() error {
	var lastError error
	if err := wait.PollUntilContextTimeout(c.ctx, 100*time.Millisecond, 1*time.Minute, true, func(ctx context.Context) (bool, error) {
		c.kubeconfigPath = filepath.Join(c.dataDir, "admin.kubeconfig")
		config, err := LoadKubeConfig(c.kubeconfigPath, "base")
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

// LoadKubeConfig loads a kubeconfig from disk. This method is
// intended to be common between fixture for servers whose lifecycle
// is test-managed and fixture for servers whose lifecycle is managed
// separately from a test run.
func LoadKubeConfig(kubeconfigPath, contextName string) (clientcmd.ClientConfig, error) {
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

	return clientcmd.NewNonInteractiveClientConfig(*rawConfig, contextName, nil, nil), nil
}

type unmanagedKCPServer struct {
	name                 string
	kubeconfigPath       string
	shardKubeconfigPaths map[string]string
	cfg                  clientcmd.ClientConfig
	shardCfgs            map[string]clientcmd.ClientConfig
	caDir                string
}

func (s *unmanagedKCPServer) CADirectory() string {
	return s.caDir
}

func (s *unmanagedKCPServer) ClientCAUserConfig(t *testing.T, config *rest.Config, name string, groups ...string) *rest.Config {
	return ClientCAUserConfig(t, config, s.caDir, name, groups...)
}

// newPersistentKCPServer returns a RunningServer for a kubeconfig
// pointing to a kcp instance not managed by the test run. Since the
// kubeconfig is expected to exist prior to running tests against it,
// the configuration can be loaded synchronously and no locking is
// required to subsequently access it.
func newPersistentKCPServer(name, kubeconfigPath string, shardKubeconfigPaths map[string]string, clientCADir string) (RunningServer, error) {
	cfg, err := LoadKubeConfig(kubeconfigPath, "base")
	if err != nil {
		return nil, err
	}

	shardCfgs := map[string]clientcmd.ClientConfig{}
	for shard, path := range shardKubeconfigPaths {
		shardCfg, err := LoadKubeConfig(path, "base")
		if err != nil {
			return nil, err
		}

		shardCfgs[shard] = shardCfg
	}

	return &unmanagedKCPServer{
		name:                 name,
		kubeconfigPath:       kubeconfigPath,
		shardKubeconfigPaths: shardKubeconfigPaths,
		cfg:                  cfg,
		shardCfgs:            shardCfgs,
		caDir:                clientCADir,
	}, nil
}

// NewFakeWorkloadServer creates a workspace in the provided server and org
// and creates a server fixture for the logical cluster that results.
func NewFakeWorkloadServer(t *testing.T, server RunningServer, org logicalcluster.Path, syncTargetName string) RunningServer {
	t.Helper()

	path, ws := NewWorkspaceFixture(t, server, org, WithName(syncTargetName+"-sink"), TODO_WithoutMultiShardSupport())
	logicalClusterName := logicalcluster.Name(ws.Spec.Cluster)
	rawConfig, err := server.RawConfig()
	require.NoError(t, err, "failed to read config for server")
	logicalConfig, kubeconfigPath := WriteLogicalClusterConfig(t, rawConfig, "base", path)
	fakeServer := &unmanagedKCPServer{
		name:           logicalClusterName.String(),
		cfg:            logicalConfig,
		kubeconfigPath: kubeconfigPath,
	}

	downstreamConfig := fakeServer.BaseConfig(t)

	// Install the required crds in the fake cluster to allow creation of the syncer deployment.
	crdClient, err := apiextensionsclient.NewForConfig(downstreamConfig)
	require.NoError(t, err)
	kubefixtures.Create(t, crdClient.ApiextensionsV1().CustomResourceDefinitions(),
		metav1.GroupResource{Group: "apps.k8s.io", Resource: "deployments"},
		metav1.GroupResource{Group: "core.k8s.io", Resource: "services"},
		metav1.GroupResource{Group: "core.k8s.io", Resource: "endpoints"},
		metav1.GroupResource{Group: "core.k8s.io", Resource: "pods"},
		metav1.GroupResource{Group: "networking.k8s.io", Resource: "ingresses"},
		metav1.GroupResource{Group: "networking.k8s.io", Resource: "networkpolicies"},
	)

	// Wait for the required crds to become ready
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)
	kubeClient, err := kubernetes.NewForConfig(downstreamConfig)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, err := kubeClient.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Logf("error seen waiting for deployment crd to become active: %v", err)
			return false
		}
		_, err = kubeClient.CoreV1().Services("").List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Logf("error seen waiting for service crd to become active: %v", err)
			return false
		}
		_, err = kubeClient.CoreV1().Endpoints("").List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Logf("error seen waiting for endpoint crd to become active: %v", err)
			return false
		}
		_, err = kubeClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Logf("error seen waiting for pods crd to become active: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	// Install the kubernetes endpoint in the default namespace. The DNS network policies reference this endpoint.
	require.Eventually(t, func() bool {
		_, err = kubeClient.CoreV1().Endpoints("default").Create(ctx, &corev1.Endpoints{
			ObjectMeta: metav1.ObjectMeta{
				Name: "kubernetes",
			},
			Subsets: []corev1.EndpointSubset{{
				Addresses: []corev1.EndpointAddress{{IP: "172.19.0.2:6443"}},
			}},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Logf("failed to create the kubernetes endpoint: %v", err)
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
	t.Helper()

	raw, err := s.cfg.RawConfig()
	require.NoError(t, err)

	config := clientcmd.NewNonInteractiveClientConfig(raw, "base", nil, nil)

	defaultConfig, err := config.ClientConfig()
	require.NoError(t, err)

	wrappedCfg := rest.CopyConfig(defaultConfig)
	wrappedCfg.QPS = -1

	return wrappedCfg
}

// RootShardSystemMasterBaseConfig returns a rest.Config for the "system:admin" context. Client-side throttling is disabled (QPS=-1).
func (s *unmanagedKCPServer) RootShardSystemMasterBaseConfig(t *testing.T) *rest.Config {
	t.Helper()

	return s.ShardSystemMasterBaseConfig(t, corev1alpha1.RootShard)
}

// ShardSystemMasterBaseConfig returns a rest.Config for the "system:admin" context of the given shard. Client-side throttling is disabled (QPS=-1).
func (s *unmanagedKCPServer) ShardSystemMasterBaseConfig(t *testing.T, shard string) *rest.Config {
	t.Helper()

	cfg, found := s.shardCfgs[shard]
	if !found {
		t.Fatalf("kubeconfig for shard %q not found", shard)
	}

	raw, err := cfg.RawConfig()
	require.NoError(t, err)

	config := clientcmd.NewNonInteractiveClientConfig(raw, "system:admin", nil, nil)

	defaultConfig, err := config.ClientConfig()
	require.NoError(t, err)

	wrappedCfg := rest.CopyConfig(defaultConfig)
	wrappedCfg.QPS = -1

	return wrappedCfg
}

func (s *unmanagedKCPServer) ShardNames() []string {
	return sets.StringKeySet(s.shardCfgs).List()
}

func (s *unmanagedKCPServer) Artifact(t *testing.T, producer func() (runtime.Object, error)) {
	t.Helper()
	artifact(t, s, producer)
}

func NoGoRunEnvSet() bool {
	envSet, _ := strconv.ParseBool(os.Getenv("NO_GORUN"))
	return envSet
}

func WaitForReady(ctx context.Context, t *testing.T, cfg *rest.Config, keepMonitoring bool) error {
	t.Logf("waiting for readiness for server at %s", cfg.Host)

	cfg = rest.CopyConfig(cfg)
	if cfg.NegotiatedSerializer == nil {
		cfg.NegotiatedSerializer = kubernetesscheme.Codecs.WithoutConversion()
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
			waitForEndpoint(ctx, t, client, endpoint)
		}(endpoint)
	}
	wg.Wait()
	t.Logf("server at %s is ready", cfg.Host)

	if keepMonitoring {
		for _, endpoint := range []string{"/livez", "/readyz"} {
			go func(endpoint string) {
				monitorEndpoint(ctx, t, client, endpoint)
			}(endpoint)
		}
	}
	return nil
}

func waitForEndpoint(ctx context.Context, t *testing.T, client *rest.RESTClient, endpoint string) {
	var lastError error
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, time.Minute, true, func(ctx context.Context) (bool, error) {
		req := rest.NewRequest(client).RequestURI(endpoint)
		_, err := req.Do(ctx).Raw()
		if err != nil {
			lastError = fmt.Errorf("error contacting %s: failed components: %v", req.URL(), unreadyComponentsFromError(err))
			return false, nil
		}

		t.Logf("success contacting %s", req.URL())
		return true, nil
	}); err != nil && lastError != nil {
		t.Error(lastError)
	}
}

func monitorEndpoint(ctx context.Context, t *testing.T, client *rest.RESTClient, endpoint string) {
	// we need a shorter deadline than the server, or else:
	// timeout.go:135] post-timeout activity - time-elapsed: 23.784917ms, GET "/livez" result: Header called after Handler finished
	if deadline, ok := t.Deadline(); ok {
		deadlinedCtx, deadlinedCancel := context.WithDeadline(ctx, deadline.Add(-20*time.Second))
		ctx = deadlinedCtx
		t.Cleanup(deadlinedCancel) // this does not really matter but govet is upset
	}
	var errCount int
	errs := sets.New[string]()
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		_, err := rest.NewRequest(client).RequestURI(endpoint).Do(ctx).Raw()
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return
		}
		// if we're noticing an error, record it and fail the test if things stay failed for two consecutive polls
		if err != nil {
			errCount++
			errs.Insert(fmt.Sprintf("failed components: %v", unreadyComponentsFromError(err)))
			if errCount == 2 {
				t.Errorf("error contacting %s: %v", endpoint, sets.List[string](errs))
			}
		}
		// otherwise, reset the counters
		errCount = 0
		if errs.Len() > 0 {
			errs = sets.New[string]()
		}
	}, 100*time.Millisecond)
}
