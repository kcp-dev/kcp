/*
Copyright 2022 The KCP Authors.

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

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	genericapiserver "k8s.io/apiserver/pkg/server"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func main() {
	if err := start(); err != nil {
		// nolint: errorlint
		if err, ok := err.(*exec.ExitError); ok {
			os.Exit(err.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err) // nolint:errcheck
		os.Exit(1)
	}
}

// Start a kcp server with the configuration expected by the e2e
// tests. Useful for developing with a persistent server.
//
// Repeatably start a persistent test server:
//
//   $ rm -rf .kcp/ && make build && ./bin/test-server 2>&1 | tee kcp.log
//
// Run the e2e suite against a persistent server:
//
//   $ TEST_ARGS='-args --use-default-kcp-server' E2E_PARALLELISM=6 make test-e2e
//
// Run individual tests against a persistent server:
//
//   $ go test -v --use-default-kcp-server
//
func start() error {
	ctx, cancelFn := context.WithCancel(genericapiserver.SetupSignalContext())
	defer cancelFn()

	commandLine := append(framework.StartKcpCommand(), framework.TestServerArgs()...)
	commandLine = append(commandLine, os.Args[1:]...)
	log.Printf("running: %v\n", strings.Join(commandLine, " "))

	cmd := exec.CommandContext(ctx, commandLine[0], commandLine[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	err := cmd.Start()
	if err != nil {
		return err
	}

	defer func() {
		cmd.Process.Kill() //nolint:errcheck
	}()

	return cmd.Wait()
}
