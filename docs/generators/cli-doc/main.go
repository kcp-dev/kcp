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
	"flag"
	"fmt"
	"log"
	"os"

	kubectlKcp "github.com/kcp-dev/kcp/cli/cmd/kubectl-kcp/cmd"
	"github.com/kcp-dev/kcp/hack/third_party/github.com/spf13/cobra/doc"
)

func main() {
	output := flag.String("output", "", "Path to output directory")
	flag.Parse()

	if output == nil || *output == "" {
		fmt.Fprintln(os.Stderr, "output is required")
		os.Exit(1)
	}

	if err := os.MkdirAll(*output, 0755); err != nil {
		log.Fatalf("Failed to re-create docs directory: %v", err)
	}

	if err := doc.GenMarkdownTree(kubectlKcp.KubectlKcpCommand(), *output); err != nil {
		log.Fatalf("Failed to generate docs: %v", err)
	}
}
