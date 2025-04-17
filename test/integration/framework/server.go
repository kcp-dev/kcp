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

package framework

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/client-go/apiextensions/client"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/embeddedetcd"

	"github.com/kcp-dev/kcp/cmd/kcp/options"
	"github.com/kcp-dev/kcp/pkg/server"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
)

// StartTestServer starts a KCP server for testing purposes.
//
// It returns a clientset for the KCP server.
//
// The returned function can be called to explicitly stop the server,
// the server is implicitly stopped when the test ends.
func StartTestServer(tb testing.TB) (kcpclientset.ClusterInterface, kcpkubernetesclientset.ClusterInterface, func()) {
	tb.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	tb.Cleanup(cancel)

	kcpOptions := options.NewOptions(tb.TempDir())

	etcdClientPort, etcdPeerPort, kcpBindPort, err := unusedPorts()
	if err != nil {
		tb.Fatalf("failed to get three available ports: %v", err)
	}

	kcpOptions.Server.EmbeddedEtcd.ClientPort = strconv.Itoa(etcdClientPort)
	kcpOptions.Server.EmbeddedEtcd.PeerPort = strconv.Itoa(etcdPeerPort)
	kcpOptions.Server.EmbeddedEtcd.Directory = filepath.Join(tb.TempDir(), "etcd")

	kcpOptions.Server.GenericControlPlane.SecureServing.BindAddress = net.ParseIP("127.0.0.1")
	kcpOptions.Server.GenericControlPlane.SecureServing.BindPort = kcpBindPort

	completedKcpOptions, err := kcpOptions.Complete()
	if err != nil {
		tb.Fatalf("failed to complete kcp options: %v", err)
	}

	if errs := completedKcpOptions.Validate(); len(errs) > 0 {
		tb.Fatalf("failed to validate kcp options: %v", errors.Join(errs...))
	}

	serverConfig, err := server.NewConfig(ctx, completedKcpOptions.Server)
	if err != nil {
		tb.Fatalf("failed to create server config: %v", err)
	}

	completedConfig, err := serverConfig.Complete()
	if err != nil {
		tb.Fatalf("failed to complete server config: %v", err)
	}

	if err := embeddedetcd.NewServer(completedConfig.EmbeddedEtcd).Run(ctx); err != nil {
		tb.Fatalf("failed to run embedded etcd server: %v", err)
	}

	s, err := server.NewServer(completedConfig)
	if err != nil {
		tb.Fatalf("failed to create server: %v", err)
	}

	go func() {
		if err := s.Run(ctx); err != nil {
			tb.Fatalf("failed to run server: %v", err)
		}
	}()

	kcpServerClientConfig := rest.CopyConfig(completedConfig.GenericConfig.LoopbackClientConfig)

	if err := wait.PollUntilContextTimeout(ctx, 1*time.Second, 60*time.Second, true,
		func(ctx context.Context) (done bool, err error) {
			healthzConfig := rest.CopyConfig(kcpServerClientConfig)
			kcpClient, err := client.NewForConfig(healthzConfig)
			if err != nil {
				// this happens because we race the API server start
				tb.Log(err)
				return false, nil
			}

			healthStatus := 0
			kcpClient.Discovery().RESTClient().Get().AbsPath("/healthz").Do(ctx).StatusCode(&healthStatus)
			if healthStatus != http.StatusOK {
				return false, nil
			}

			return true, nil
		},
	); err != nil {
		tb.Fatal(err)
	}

	kcpClusterClient, err := kcpclientset.NewForConfig(kcpServerClientConfig)
	if err != nil {
		tb.Fatal(err)
	}

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpServerClientConfig)
	if err != nil {
		tb.Fatal(err)
	}

	return kcpClusterClient, kubeClusterClient, cancel
}
