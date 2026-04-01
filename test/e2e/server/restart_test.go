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

package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestKcpRestart validates that a kcp instance can be stopped and restarted
// with the same data directory and that data written before the restart
// survives.
func TestKcpRestart(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	srv := &kcpInstance{}
	t.Cleanup(func() { srv.stop(t) })

	t.Log("Start kcp")
	srv.start(t)

	cfg := srv.adminConfig(t)
	kubeClient := kubernetes.NewForConfigOrDie(cfg)

	t.Log("Write a ConfigMap")
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "restart-test"},
		Data:       map[string]string{"key": "value"},
	}
	_, err := kubeClient.CoreV1().ConfigMaps("default").Create(t.Context(), cm, metav1.CreateOptions{})
	require.NoError(t, err, "creating ConfigMap before restart")

	t.Log("Stopping kcp")
	srv.stop(t)

	t.Log("Starting kcp again")
	srv.start(t)

	cfg = srv.adminConfig(t)
	kubeClient = kubernetes.NewForConfigOrDie(cfg)

	t.Log("Read ConfigMap")
	got, err := kubeClient.CoreV1().ConfigMaps("default").Get(t.Context(), "restart-test", metav1.GetOptions{})
	require.NoError(t, err, "getting ConfigMap after restart")
	require.Equal(t, "value", got.Data["key"])
}

// kcpInstance is mostly a copy from the kcptesting harness, but allows
// stopping and restarting the instance. The kcptesting harness has to
// adhere to the RunningServer interface, so adding a function for all
// servers that implement the RunningServer for a single test is
// overkill.
type kcpInstance struct {
	dataDir     string
	artifactDir string

	startCount int
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	done       <-chan error
}

func (k *kcpInstance) start(t *testing.T) {
	t.Helper()

	k.startCount++

	if k.dataDir == "" {
		var err error
		k.artifactDir, k.dataDir, err = kcptestingserver.ScratchDirs(t)
		require.NoError(t, err)
		t.Logf("Data dir: %v", k.dataDir)
		t.Logf("Artifact dir: %v", k.artifactDir)
	}

	kubeconfigPath := filepath.Join(k.dataDir, "admin.kubeconfig")
	tokenStorePath := filepath.Join(k.dataDir, ".admin-token-store")

	// Remove old kubeconfig and token store so we wait for the new
	// instance to write fresh ones.
	_ = os.Remove(kubeconfigPath)
	_ = os.Remove(tokenStorePath)

	securePort, err := kcptestingserver.GetFreePort(t)
	require.NoError(t, err)
	etcdClientPort, err := kcptestingserver.GetFreePort(t)
	require.NoError(t, err)
	etcdPeerPort, err := kcptestingserver.GetFreePort(t)
	require.NoError(t, err)

	label := fmt.Sprintf("boot-%d", k.startCount)
	workdir, commandArgs := kcptestingserver.StartKcpCommand(label)
	commandArgs = append(commandArgs,
		"--root-directory", k.dataDir,
		"--secure-port="+securePort,
		"--embedded-etcd-client-port="+etcdClientPort,
		"--embedded-etcd-peer-port="+etcdPeerPort,
		"--embedded-etcd-wal-size-bytes="+strconv.Itoa(5*1000),
		"--kubeconfig-path="+kubeconfigPath,
		"--bind-address=127.0.0.1",
		"--v=4",
	)

	ctx, cancel := context.WithCancel(t.Context())
	k.cancel = cancel

	cmd := exec.CommandContext(context.Background(), commandArgs[0], commandArgs[1:]...)
	cmd.Dir = workdir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	k.cmd = cmd

	logPath := filepath.Join(k.artifactDir, fmt.Sprintf("kcp-%s.log", label))
	logFile, err := os.Create(logPath)
	require.NoError(t, err)
	t.Cleanup(func() { logFile.Close() })

	var buf bytes.Buffer
	w := io.MultiWriter(&buf, logFile)
	cmd.Stdout = w
	cmd.Stderr = w

	require.NoError(t, cmd.Start(), "starting kcp (%s)", label)

	go func() {
		<-ctx.Done()
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	k.done = done

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		fi, err := os.Stat(kubeconfigPath)
		if err != nil {
			return false, err.Error()
		}
		if fi.Size() == 0 {
			return false, "file exists but is empty"
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	cfg := k.adminConfig(t)

	require.NoError(t, kcptestingserver.WaitForReady(ctx, cfg), "waiting for readiness (%s)", label)

	t.Logf("kcp (%s) is ready on port %s", label, securePort)
}

func (k *kcpInstance) stop(t *testing.T) {
	if k.cancel == nil {
		return
	}
	k.cancel()
	k.cancel = nil
	select {
	case <-k.done:
	case <-time.After(wait.ForeverTestTimeout):
		t.Fatal("timed out waiting for kcp to stop")
	}
}

func (k *kcpInstance) adminConfig(t *testing.T) *rest.Config {
	t.Helper()

	kubeconfigPath := filepath.Join(k.dataDir, "admin.kubeconfig")
	raw, err := clientcmd.LoadFromFile(kubeconfigPath)
	require.NoError(t, err)

	cfg, err := clientcmd.NewNonInteractiveClientConfig(*raw, "root", nil, nil).ClientConfig()
	require.NoError(t, err)
	cfg.QPS = -1

	return cfg
}
