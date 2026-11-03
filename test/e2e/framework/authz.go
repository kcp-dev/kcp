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
	"path/filepath"
	"strconv"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/test/server"
)

// AuthzWebhook is an in-process authorization webhook.
type AuthzWebhook struct {
	// Handler is the decision handler. Can be swapped at runtime.
	Handler *server.AuthzHandler
	// Args are the kcp shard arguments wiring this webhook in.
	Args []string
}

// StartAuthzWebhook serves fn as an authorization webhook over TLS on an ephemeral port.
func StartAuthzWebhook(ctx context.Context, t kcptesting.TestingT, fn server.AuthzHandlerFunc) *AuthzWebhook {
	t.Helper()

	port, err := kcptestingserver.GetFreePort(t)
	if err != nil {
		t.Fatalf("failed to resolve free port: %v", err)
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("free port %q is not numeric: %v", port, err)
	}

	handler := server.NewAuthzHandler(fn)
	srv, err := server.NewTLSServer(portNum, []string{"localhost"}, t.TempDir(), handler)
	require.NoError(t, err)
	require.NoError(t, srv.Start(ctx))

	t.Cleanup(func() {
		assert.NoError(t, srv.Stop())
	})

	return &AuthzWebhook{
		Handler: handler,
		Args: []string{
			"--authorization-webhook-config-file", webhookConfigFile(t, srv.CAFile(), port),
			// Defaults are 5m and 30s; a handler swapped mid-test would be shadowed by cached decisions.
			"--authorization-webhook-cache-authorized-ttl=0s",
			"--authorization-webhook-cache-unauthorized-ttl=0s",
		},
	}
}

func webhookConfigFile(t kcptesting.TestingT, caFile, port string) string {
	t.Helper()

	kubeconfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"webhook": {
				Server:               "https://localhost:" + port,
				CertificateAuthority: caFile,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"webhook": {Cluster: "webhook"},
		},
		CurrentContext: "webhook",
	}

	configPath := filepath.Join(t.TempDir(), "webhook.yaml")
	if err := clientcmd.WriteToFile(kubeconfig, configPath); err != nil {
		t.Fatalf("failed to write webhook configuration: %v", err)
	}

	return configPath
}
