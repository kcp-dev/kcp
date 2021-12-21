/*
Copyright 2021 The KCP Authors.

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
	goflag "flag"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	genericapiserver "k8s.io/apiserver/pkg/server"

	virtualgenericcmd "github.com/kcp-dev/kcp/pkg/virtual/generic/cmd"
	virtualworkspacescmd "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cmd"
)

func main() {
	stopCh := genericapiserver.SetupSignalHandler()

	rand.Seed(time.Now().UTC().UnixNano())

	pflag.CommandLine.AddGoFlagSet(goflag.CommandLine)

	command := NewVirtualWorkspaceApiServerCommand(stopCh)
	if err := command.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func NewVirtualWorkspaceApiServerCommand(stopCh <-chan struct{}) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "virtual-workspaces",
		Short: "Command for virtual workspaces API Servers",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
			os.Exit(1)
		},
	}
	workspacesAPIServerSubCommandOptions := &virtualworkspacescmd.WorkspacesSubCommandOptions{}
	workspacesAPIServerSubCommand := virtualgenericcmd.APIServerCommand(os.Stdout, os.Stderr, stopCh, workspacesAPIServerSubCommandOptions)
	cmd.AddCommand(workspacesAPIServerSubCommand)

	return cmd
}
