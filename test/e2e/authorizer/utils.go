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
	"os/exec"
	"testing"
	"time"
)

func RunWebhook(ctx context.Context, t *testing.T, response string) context.CancelFunc {
	args := []string{
		"--tls",
		"--response", response,
		"--pki-directory", fmt.Sprintf(".%s", t.Name()),
	}

	t.Logf("Starting webhook with %s policy...", response)

	ctx, cancel := context.WithCancel(ctx)

	cmd := exec.CommandContext(ctx, "httest", args...)
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("Failed to start webhook: %v", err)
	}

	// give httest a moment to boot up
	time.Sleep(2 * time.Second)

	return func() {
		t.Log("Stopping webhook...")
		cancel()
		// give it some time to shutdown
		time.Sleep(2 * time.Second)
	}
}
