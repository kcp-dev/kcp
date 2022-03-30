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
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// Start a kcp server with the configuration expected by the e2e
// tests. Useful for developing with a persistent server.
//
// Repeatably start a persistent test server:
//
//   $ rm -rf .kcp/ && make build && ./bin/test-server 2>&1 | tee kcp.log
//
// Run the e2e suite against a persistent server:
//
//   $ TEST_ARGS='-args --use-default-server' E2E_PARALLELISM=6 make test-e2e
//
// Run individual tests against a persistent server:
//
//   $ go test -v --use-default-server
//
func main() {
	commandLine := append(framework.StartKcpCommand(), framework.TestServerArgs()...)
	log.Printf("running: %v\n", strings.Join(commandLine, " "))

	cmd := exec.Command(commandLine[0], commandLine[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	err := cmd.Start()
	if err != nil {
		log.Fatal(err)
	}

	// Ensure child process is killed when the parent is exiting
	defer func() {
		err := cmd.Process.Kill()
		if err != nil {
			log.Fatal(err)
		}
	}()

	err = cmd.Wait()
	if err != nil {
		log.Fatal(err)
	}
}
