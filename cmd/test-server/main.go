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

	"github.com/kcp-dev/kcp/cmd/sharded-test-server/third_party/library-go/crypto"
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
	quiet := flag.Bool("quiet", false, "Suppress output of the subprocesses")

	// split flags into --shard-* and everything else (generic). The former are
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

	if err := start(shardFlags, *quiet); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func start(shardFlags []string, quiet bool) error {
	ctx, cancelFn := context.WithCancel(genericapiserver.SetupSignalContext())
	defer cancelFn()

	// create client CA and kcp-admin client cert to connect through front-proxy
	_, err := crypto.MakeSelfSignedCA(
		filepath.Join(".kcp", "/client-ca.crt"),
		filepath.Join(".kcp", "/client-ca.key"),
		filepath.Join(".kcp", "/client-ca-serial.txt"),
		"kcp-client-ca",
		365,
	)
	if err != nil {
		return fmt.Errorf("failed to create client-ca: %w", err)
	}

	logFilePath := flag.Lookup("log-file-path").Value.String()
	shard := shard.NewShard(
		"kcp",
		".kcp",
		logFilePath,
		append(shardFlags,
			"--audit-log-path", filepath.Join(filepath.Dir(logFilePath), "audit.log"),
			"--client-ca-file", filepath.Join(".kcp", "client-ca.crt"),
		),
	)
	if err := shard.Start(ctx, quiet); err != nil {
		return err
	}

	errCh, err := shard.WaitForReady(ctx)
	if err != nil {
		return err
	}

	return <-errCh
}
