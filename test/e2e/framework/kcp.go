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
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// kcpServer exposes a kcp invocation to a test and
// ensures the following semantics:
//  - the server will run only until the test deadline
//  - all ports and data directories are unique to support
//    concurrent execution within a test case and across tests
type kcpServer struct {
	args        []string
	ctx         context.Context
	artifactDir string

	lock *sync.Mutex
	cfg  *rest.Config

	t TestingTInterface
}

func (c *kcpServer) ArtifactDir() string {
	return c.artifactDir
}

func newKcpServer(t *T, args ...string) (*kcpServer, error) {
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
	artifactDir, err := ArtifactDir(t, kcpListenPort)
	if err != nil {
		return nil, err
	}
	return &kcpServer{
		args: append([]string{
			"--root_directory=" + artifactDir,
			"--listen=:" + kcpListenPort,
			"--etcd_client_port=" + etcdClientPort,
			"--etcd_peer_port=" + etcdPeerPort,
			"--kubeconfig_path=admin.kubeconfig"},
			args...),
		artifactDir: artifactDir,
		ctx:         ctx,
		t:           t,
		lock:        &sync.Mutex{},
	}, nil
}

// Run runs the kcp server while the parent context is active. This call is not blocking,
// callers should ensure that the server is Ready() before using it.
func (c *kcpServer) Run(parentCtx context.Context) error {
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
	cmd := exec.CommandContext(ctx, "kcp", append([]string{"start"}, c.args...)...)
	c.t.Logf("running: %v", strings.Join(cmd.Args, " "))
	logFile, err := os.Create(filepath.Join(c.artifactDir, "kcp.log"))
	if err != nil {
		cleanupCancel()
		return fmt.Errorf("could not create log file: %w", err)
	}
	log := bytes.Buffer{}
	writers := []io.Writer{&log, logFile}
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
			c.t.Errorf("`kcp` failed: %w logs:\n%v", err, data)
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
			// TODO: a rebase of our k8s fork on something newer will remove the following messages
			[]byte(`flowcontrol.apiserver.k8s.io/v1beta1 FlowSchema is deprecated in v1.23+, unavailable in v1.26+`),
			[]byte(`flowcontrol.apiserver.k8s.io/v1beta1 PriorityLevelConfiguration is deprecated in v1.23+, unavailable in v1.26+`),
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

// Config exposes a copy of the client config for this server.
func (c *kcpServer) Config() *rest.Config {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.cfg == nil {
		return nil
	}
	return rest.CopyConfig(c.cfg)
}

// Ready blocks until the server is healthy and ready. Before returning,
// goroutines are started to ensure that the test is failed if the server
// does not remain so.
func (c *kcpServer) Ready() error {
	if err := c.loadCfg(); err != nil {
		return err
	}
	if c.ctx.Err() != nil {
		// cancelling the context will preempt derivative calls but not this
		// main Ready() body, so we check before continuing that we are live
		return fmt.Errorf("failed to wait for readiness: %w", c.ctx.Err())
	}
	cfg := c.Config()
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

	for _, endpoint := range []string{"/livez", "/readyz"} {
		go func(endpoint string) {
			c.monitorEndpoint(client, endpoint)
		}(endpoint)
	}
	return nil
}

func (c *kcpServer) loadCfg() error {
	var loadError error
	loadCtx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
	wait.UntilWithContext(loadCtx, func(ctx context.Context) {
		kubeconfigPath := filepath.Join(c.artifactDir, "admin.kubeconfig")
		if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
			return // try again
		} else if err != nil {
			loadError = fmt.Errorf("failed to read admin kubeconfig after kcp start: %w", err)
			return
		}
		rawConfig, err := clientcmd.LoadFromFile(kubeconfigPath)
		if err != nil {
			loadError = fmt.Errorf("failed to load admin kubeconfig: %w", err)
			return
		}
		config, err := clientcmd.NewNonInteractiveClientConfig(*rawConfig, "admin", nil, nil).ClientConfig()
		if err != nil {
			loadError = fmt.Errorf("failed to load cluster-less client config: %w", err)
			return
		}
		c.lock.Lock()
		c.cfg = config
		c.lock.Unlock()
		cancel()
	}, 100*time.Millisecond)
	return loadError
}

func (c *kcpServer) waitForEndpoint(client *rest.RESTClient, endpoint string) {
	var lastMsg string
	var succeeded bool
	loadCtx, cancel := context.WithTimeout(c.ctx, 15*time.Second)
	wait.UntilWithContext(loadCtx, func(ctx context.Context) {
		_, err := rest.NewRequest(client).RequestURI(endpoint).Do(ctx).Raw()
		if err == nil {
			c.t.Logf("success contacting %s", endpoint)
			cancel()
			succeeded = true
		} else {
			lastMsg = fmt.Sprintf("error contacting %s: %v", endpoint, err)
		}
	}, 100*time.Millisecond)
	if !succeeded {
		c.t.Errorf(lastMsg)
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
		if err != nil {
			c.t.Errorf("error contacting %s: %v", endpoint, err)
		}
	}, 1*time.Second)
}
