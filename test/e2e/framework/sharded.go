/*
Copyright 2026 The kcp Authors.

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
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"
)

// SerialSuite is the suite of tests that cannot share a server with other
// tests, e.g. because they rotate the identities of kcp's core APIs or
// otherwise disrupt every workspace. Tests of this suite start a server of
// their own with PrivateShardedKcpServer and run one at a time: the e2e
// targets run the suite with -p 1 -parallel 1 after the parallel suites,
// once the shared server is stopped (make test-e2e-serial).
const SerialSuite = "serial"

// shardedServerStartTimeout bounds the start of a sharded-test-server,
// including compiling it when running via `go run`.
const shardedServerStartTimeout = 5 * time.Minute

// PrivateShardedKcpServer starts a sharded-test-server, i.e. a cache server,
// the given number of shards and a front-proxy, for this test alone and
// stops it when the test ends. Shards are named like the e2e targets name
// them: "root", "shard-1", ... The returned server is addressed through the
// front-proxy; ShardSystemMasterBaseConfig reaches the individual shards.
//
// sharded-test-server listens on fixed ports, so only one can run at a
// time: this is for tests of the SerialSuite.
func PrivateShardedKcpServer(t *testing.T, shards int) kcptestingserver.RunningServer {
	t.Helper()
	require.Positive(t, shards, "a sharded server needs at least one shard")

	artifactDir, dataDir, err := kcptestingserver.ScratchDirs(t)
	require.NoError(t, err, "failed to create scratch dirs")

	workDir, commandLine := kcptestingserver.Command("sharded-test-server", t.Name())
	commandLine = append(commandLine,
		"--quiet",
		"--v=2",
		"--number-of-shards="+strconv.Itoa(shards),
		"--work-dir-path="+dataDir,
		"--log-dir-path="+artifactDir,
		"--shard-run-virtual-workspaces=false",
		"--shard-feature-gates="+TestFeatureGates,
		"--proxy-feature-gates="+TestFeatureGates,
	)

	logPath := filepath.Join(artifactDir, "sharded-test-server.log")
	logFile, err := os.Create(logPath)
	require.NoError(t, err, "failed to create the sharded-test-server log file")

	// the context is never cancelled on purpose: cancelling would SIGKILL
	// only the direct child, leaving the shards, the cache server and the
	// front-proxy behind (and in `go run` mode even sharded-test-server
	// itself). The process group gets a SIGTERM instead, which
	// sharded-test-server turns into an orderly shutdown of its components.
	cmd := exec.CommandContext(context.Background(), commandLine[0], commandLine[1:]...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Env = append(os.Environ(), "UNSAFE_E2E_HACK_DISABLE_ETCD_FSYNC=true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	t.Logf("Starting sharded-test-server with %d shard(s) in %s, logs in %s", shards, dataDir, artifactDir)
	require.NoError(t, cmd.Start(), "failed to start sharded-test-server")

	exited := make(chan error, 1)
	go func() {
		exited <- cmd.Wait()
	}()
	t.Cleanup(func() {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil && !strings.Contains(err.Error(), "no such process") {
			t.Logf("failed to stop sharded-test-server: %v", err)
		}
		select {
		case <-exited:
		case <-time.After(time.Minute):
			t.Logf("sharded-test-server did not stop within a minute, killing it")
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-exited
		}
		_ = logFile.Close()
	})

	// sharded-test-server writes this file once every component is ready.
	readyFile := filepath.Join(dataDir, ".kcp", "ready-to-test")
	deadline := time.After(shardedServerStartTimeout)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ready := false; !ready; {
		select {
		case err := <-exited:
			// the channel is drained for Cleanup, which waits on it.
			exited <- err
			require.FailNowf(t, "sharded-test-server exited before it was ready", "%v\n%s", err, tailOf(logPath, 40))
		case <-deadline:
			require.FailNowf(t, "sharded-test-server did not become ready", "no %s after %s\n%s", readyFile, shardedServerStartTimeout, tailOf(logPath, 40))
		case <-ticker.C:
			_, err := os.Stat(readyFile)
			ready = err == nil
		}
	}

	shardKubeconfigs := map[string]string{corev1alpha1.RootShard: filepath.Join(dataDir, ".kcp-0", "admin.kubeconfig")}
	for i := 1; i < shards; i++ {
		shardKubeconfigs[fmt.Sprintf("shard-%d", i)] = filepath.Join(dataDir, fmt.Sprintf(".kcp-%d", i), "admin.kubeconfig")
	}
	server, err := kcptestingserver.NewExternalKCPServer(t.Name(), filepath.Join(dataDir, ".kcp", "admin.kubeconfig"), shardKubeconfigs, filepath.Join(dataDir, ".kcp"))
	require.NoError(t, err, "failed to create the server fixture")

	for _, shard := range server.ShardNames() {
		cfg := server.ShardSystemMasterBaseConfig(t, shard)
		require.NoError(t, kcptestingserver.WaitForReady(t.Context(), cfg), "shard %s is not ready", shard)
		kcptestingserver.MonitorEndpoints(t, cfg, "/livez", "/readyz")
	}

	// the front-proxy reports ready before its workspace index is populated;
	// requests by path fail until then. Wait until the root workspace resolves.
	client, err := kcpclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := client.Cluster(core.RootCluster.Path()).TenancyV1alpha1().Workspaces().List(context.Background(), metav1.ListOptions{})
		return err == nil, fmt.Sprintf("listing workspaces in root through the front-proxy: %v", err)
	}, time.Minute, 500*time.Millisecond, "the front-proxy does not serve the root workspace")

	return server
}

// tailOf returns the last n lines of the file, for error messages.
func tailOf(path string, n int) string {
	bs, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(cannot read %s: %v)", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(bs), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
