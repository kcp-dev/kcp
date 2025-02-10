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
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func RunWebhook(ctx context.Context, t *testing.T, port string, response string) context.CancelFunc {
	t.Logf("Starting webhook with %s policy...", response)
	address := fmt.Sprintf("localhost:%s", port)

	ctx, cancel := context.WithCancel(ctx)
	pkiDir := fmt.Sprintf("testdata/.%s", t.Name())
	args := []string{
		"--tls",
		"--response", response,
		"--pki-directory", pkiDir,
		"--listen", address,
	}

	cmd := exec.CommandContext(ctx, "httest", args...)
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("Failed to start webhook: %v", err)
	}

	framework.Eventually(t, func() (bool, string) {
		caCertPath := fmt.Sprintf("%s/ca.crt", pkiDir)
		if _, err := os.Stat(caCertPath); os.IsNotExist(err) {
			return false, "ca.crt file does not exist"
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	framework.Eventually(t, func() (bool, string) {
		conn, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			return false, fmt.Sprintf("Webhook is not serving on %s: %v", address, err)
		}
		_ = conn.Close()
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*200)

	return func() {
		t.Log("Stopping webhook...")
		cancel()
		// give it some time to shutdown
		time.Sleep(2 * time.Second)
	}
}
