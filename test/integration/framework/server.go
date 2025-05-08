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
	"testing"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"

	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestingserver "github.com/kcp-dev/kcp/sdk/testing/server"

	// Set kcptestingserver.ContextRunInProcessFunc
	_ "github.com/kcp-dev/kcp/test/e2e/framework"
)

// StartTestServer starts a KCP server for testing purposes.
//
// It returns a clientset for the KCP server.
//
// The returned function can be called to explicitly stop the server,
// the server is implicitly stopped when the test ends.
func StartTestServer(tb testing.TB, opts ...kcptestingserver.Option) (kcptestingserver.RunningServer, kcpclientset.ClusterInterface, kcpkubernetesclientset.ClusterInterface) {
	tb.Helper()

	server := kcptesting.PrivateKcpServer(
		tb,
		append(
			[]kcptestingserver.Option{
				kcptestingserver.WithDefaultsFrom(tb),
				kcptestingserver.WithRunInProcess(),
			},
			opts...,
		)...,
	)

	kcpClusterClient, err := kcpclientset.NewForConfig(server.BaseConfig(tb))
	if err != nil {
		tb.Fatal(err)
	}

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(server.BaseConfig(tb))
	if err != nil {
		tb.Fatal(err)
	}

	return server, kcpClusterClient, kubeClusterClient
}
