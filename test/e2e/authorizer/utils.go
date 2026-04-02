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

package authorizer

import (
	"context"
	"net/http"
	"testing"

	"github.com/kcp-dev/kcp/test/server"
)

func RunWebhook(ctx context.Context, t *testing.T, port int, handler http.Handler) (string, context.CancelFunc) {
	t.Helper()
	t.Logf("Starting webhook on port %d...", port)

	srv, err := server.NewTLSServer(port, []string{"localhost"}, t.TempDir(), handler)
	if err != nil {
		t.Fatalf("Failed to create webhook server: %v", err)
	}

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Failed to start webhook: %v", err)
	}

	return srv.CAFile(), func() {
		t.Log("Stopping webhook...")
		if err := srv.Stop(); err != nil {
			t.Logf("Error stopping webhook: %v", err)
		}
	}
}
