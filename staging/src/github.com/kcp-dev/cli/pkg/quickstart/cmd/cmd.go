/*
Copyright 2026 The kcp Authors.

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

package cmd

import (
	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/kcp-dev/cli/pkg/quickstart/plugin"
	"github.com/kcp-dev/sdk/cmd/help"
)

func New(streams genericclioptions.IOStreams) *cobra.Command {
	opts := plugin.NewQuickstartOptions(streams)
	cmd := &cobra.Command{
		Use:   "quickstart",
		Short: "Bootstrap a demo kcp environment with workspaces, APIs, and bindings",
		Long: help.Doc(`
			Bootstrap a kcp environment for demos, feature development, or learning.

			This command automates the manual steps from the kcp tenancy & APIs
			quickstart. It creates workspaces, APIResourceSchemas, APIExports,
			and APIBindings in a single command.

			Scenarios:
			  api-provider   Create a service provider workspace with a sample API
			                 (cowboys) and a consumer workspace with a binding to it.
			                 (default)

			Use --cleanup to tear down all resources created by a previous run.
		`),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := opts.Complete(args); err != nil {
				return err
			}
			if err := opts.Validate(); err != nil {
				return err
			}
			return opts.Run(cmd.Context())
		},
	}

	opts.BindFlags(cmd)
	return cmd
}
