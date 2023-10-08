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
	"os"

	"github.com/spf13/pflag"

	genericapiserver "k8s.io/apiserver/pkg/server"

	virtualworkspacecommand "github.com/kcp-dev/kcp/cmd/virtual-workspaces/command"
)

func main() {
	ctx := genericapiserver.SetupSignalContext()

	pflag.CommandLine.AddGoFlagSet(goflag.CommandLine)

	command := virtualworkspacecommand.NewCommand(ctx, os.Stderr)
	if err := command.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
