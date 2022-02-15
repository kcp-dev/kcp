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
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egymgmbh/go-prefix-writer/prefixer"
	"github.com/spf13/pflag"

	apierrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/kcp/pkg/server"
	"github.com/kcp-dev/kcp/pkg/server/options"
)

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
	// TODO: remove once https://github.com/kcp-dev/kcp/issues/301 is fixed
	rawCfg *clientcmdapi.Config

	t *testing.T
}

func newKcpServer(t *testing.T, cfg KcpConfig, artifactDir, dataDir string) (*kcpServer, error) {
	t.Helper()
	ctx := context.Background()
	if deadline, ok := t.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < 30*time.Second {
			return nil, fmt.Errorf("only have %v until deadline, need at least 30 seconds", remaining)
		}
		c, cancel := context.WithDeadline(ctx, deadline.Add(-10*time.Second))
		ctx = c
		t.Cleanup(cancel) // this does not really matter but govet is upset
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
			"--kubeconfig-path=admin.kubeconfig",
		},
			cfg.Args...),
		dataDir:     dataDir,
		artifactDir: artifactDir,
		ctx:         ctx,
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

// Run runs the kcp server while the parent context is active. This call is not blocking,
// callers should ensure that the server is Ready() before using it.
func (c *kcpServer) Run(parentCtx context.Context, opts ...RunOption) error {
	runOpts := runOptions{}
	for _, opt := range opts {
		opt(&runOpts)
	}

	// calling any methods on *testing.T after the test is finished causes
	// a panic, so we need to communicate to our cleanup routines when the
	// test has been completed, and we need to communicate back up to the
	// test when we're done with everything
	ctx, cancel := context.WithCancel(parentCtx)
	if deadline, ok := c.t.Deadline(); ok {
		deadlinedCtx, deadlinedCancel := context.WithDeadline(ctx, deadline.Add(-10*time.Second))
		ctx = deadlinedCtx
		c.t.Cleanup(deadlinedCancel) // this does not really matter but govet is upset
	}
	c.ctx = ctx
	cleanupCtx, cleanupCancel := context.WithCancel(context.Background())
	c.t.Cleanup(func() {
		c.t.Log("cleanup: ending kcp server")
		cancel()
		<-cleanupCtx.Done()
	})

	c.t.Logf("running: %v", strings.Join(append([]string{"kcp", "start"}, c.args...), " "))

	// run kcp start in-process for easier debugging
	if runOpts.runInProcess {
		serverOptions := options.NewOptions()
		all := pflag.NewFlagSet("kcp", pflag.ContinueOnError)
		for _, fs := range serverOptions.Flags().FlagSets {
			all.AddFlagSet(fs)
		}
		if err := all.Parse(c.args); err != nil {
			cleanupCancel()
			return err
		}

		completed, err := serverOptions.Complete()
		if err != nil {
			cleanupCancel()
			return err
		}
		if errs := completed.Validate(); len(errs) > 0 {
			cleanupCancel()
			return apierrors.NewAggregate(errs)
		}

		s, err := server.NewServer(completed)
		if err != nil {
			cleanupCancel()
			return err
		}
		go func() {
			defer func() { cleanupCancel() }()
			if err := s.Run(ctx); err != nil && ctx.Err() == nil {
				c.t.Errorf("`kcp` failed: %v", err)
			}
		}()

		return nil
	}

	cmd := exec.CommandContext(ctx, "kcp", append([]string{"start"}, c.args...)...)
	logFile, err := os.Create(filepath.Join(c.artifactDir, "kcp.log"))
	if err != nil {
		cleanupCancel()
		return fmt.Errorf("could not create log file: %w", err)
	}
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
		cleanupCancel()
		return err
	}
	go func() {
		defer func() { cleanupCancel() }()
		err := cmd.Wait()
		data := c.filterKcpLogs(&log)
		if err != nil && ctx.Err() == nil {
			// we care about errors in the process that did not result from the
			// context expiring and us ending the process
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
		_, err := output.Write(append(line, []byte(`\n`)...))
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

// Config exposes a copy of the client config for this server.
func (c *kcpServer) Config() (*rest.Config, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.cfg == nil {
		return nil, fmt.Errorf("programmer error: kcpServer.Config() called before load succeeded. Stack: %s", string(debug.Stack()))
	}
	// TODO: remove once https://github.com/kcp-dev/kcp/issues/301 is fixed
	r, e := c.cfg.ClientConfig()
	if e != nil {
		c.t.Logf("%#v", c.rawCfg)
	}
	return r, e
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
	cfg, err := c.Config()
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
		if fs, err := os.Stat(c.kubeconfigPath); os.IsNotExist(err) {
			lastError = err
			return false, nil
		} else if err != nil {
			lastError = fmt.Errorf("failed to read admin kubeconfig after kcp start: %w", err)
			return false, nil
		} else if fs.Size() == 0 {
			return false, nil
		}

		rawConfig, err := clientcmd.LoadFromFile(c.kubeconfigPath)
		if err != nil {
			lastError = fmt.Errorf("failed to load admin kubeconfig: %w", err)
			return false, nil
		}

		config := clientcmd.NewNonInteractiveClientConfig(*rawConfig, "root", nil, nil)

		c.lock.Lock()
		c.cfg = config
		c.rawCfg = rawConfig // TODO: remove once https://github.com/kcp-dev/kcp/issues/301 is fixed
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
			lastError = fmt.Errorf("error contacting %s: %w", req.URL(), err)
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
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		_, err := rest.NewRequest(client).RequestURI(endpoint).Do(ctx).Raw()
		if errors.Is(err, context.Canceled) || c.ctx.Err() != nil {
			return
		}
		if err != nil {
			c.t.Errorf("error contacting %s: %v", endpoint, err)
		}
	}, 1*time.Second)
}
