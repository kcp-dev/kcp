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
	goflags "flag"
	"fmt"
	"os"

	"github.com/spf13/pflag"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/cli/pkg/workspace/cmd"
)

func main() {
	flags := pflag.NewFlagSet("kubectl-create-workspace", pflag.ExitOnError)
	pflag.CommandLine = flags

	createWorkspaceCmd, err := cmd.NewCreate("kubectl create workspace", genericclioptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	// setup klog
	fs := goflags.NewFlagSet("klog", goflags.PanicOnError)
	klog.InitFlags(fs)
	createWorkspaceCmd.PersistentFlags().AddGoFlagSet(fs)

	if err := createWorkspaceCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
