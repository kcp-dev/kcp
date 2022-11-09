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
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	genericapiserver "k8s.io/apiserver/pkg/server"

	shard "github.com/kcp-dev/kcp/cmd/test-server/kcp"
)

// Start a kcp server with the configuration expected by the e2e
// tests. Useful for developing with a persistent server.
//
// Repeatably start a persistent test server:
//
//	$ rm -rf .kcp/ && make build && ./bin/test-server 2>&1 | tee kcp.log
//
// Run the e2e suite against a persistent server:
//
//	$ TEST_ARGS='-args --use-default-kcp-server' E2E_PARALLELISM=6 make test-e2e
//
// Run individual tests against a persistent server:
//
//	$ go test -v --use-default-kcp-server
func main() {
	flag.String("log-file-path", ".kcp/kcp.log", "Path to the log file")

	// split flags into --shard-* and everything elese (generic). The former are
	// passed to the respective components. Everything after "--" is considered a shard flag.
	var shardFlags, genericFlags []string
	for i, arg := range os.Args[1:] {
		if arg == "--" {
			shardFlags = append(shardFlags, os.Args[i+2:]...)
			break
		}
		if strings.HasPrefix(arg, "--shard-") {
			shardFlags = append(shardFlags, "-"+strings.TrimPrefix(arg, "--shard"))
		} else {
			genericFlags = append(genericFlags, arg)
		}
	}
	flag.CommandLine.Parse(genericFlags) //nolint:errcheck

	if err := start(shardFlags); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func start(shardFlags []string) error {
	ctx, cancelFn := context.WithCancel(genericapiserver.SetupSignalContext())
	defer cancelFn()

	logFilePath := flag.Lookup("log-file-path").Value.String()
	shard := shard.NewShard(
		"kcp",
		".kcp",
		logFilePath,
		append(shardFlags, "--audit-log-path", filepath.Join(filepath.Dir(logFilePath), "audit.log")),
	)
	if err := shard.Start(ctx); err != nil {
		return err
	}

	errCh, err := shard.WaitForReady(ctx)
	if err != nil {
		return err
	}

	return <-errCh
}
