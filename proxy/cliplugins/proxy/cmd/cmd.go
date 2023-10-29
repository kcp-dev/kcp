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

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/kcp-dev/kcp/proxy/cliplugins/proxy/plugin"
)

var (
	proxyExample = `
	# Ensure a proxy is running on the specified proxy target.
	%[1]s proxy create <proxy-target-name> --proxy-image <kcp-proxy-image> -o proxy.yaml
	KUBECONFIG=<pcluster-config> kubectl apply -f proxy.yaml

	# Directly apply the manifest
	%[1]s proxy create <proxy-target-name> --proxy-image <kcp-proxy-image> -o - | KUBECONFIG=<pcluster-config> kubectl apply -f -
`
)

// New provides a cobra command for proxy operations.
func New(streams genericclioptions.IOStreams) (*cobra.Command, error) {
	cmd := &cobra.Command{
		Aliases:          []string{"proxy-target", "proxy-targets"},
		Use:              "create <proxy-target-name> --proxy-image <kcp-proxy-image> -o <output-file>",
		Short:            "Manages KCP proxy targets",
		SilenceUsage:     true,
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	// Proxy command
	opts := plugin.NewProxyOptions(streams)

	enableProxyCmd := &cobra.Command{
		Use:          "create <proxy-target-name> --proxy-image <kcp-proxy-image> -o <output-file>",
		Short:        "Create a proxy in kcp with service account and RBAC permissions. Output a manifest to deploy a proxy for the given proxy target in a physical cluster.",
		Example:      fmt.Sprintf(proxyExample, "kubectl"),
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 1 {
				return c.Help()
			}

			if err := opts.Complete(args); err != nil {
				return err
			}

			if err := opts.Validate(); err != nil {
				return err
			}

			return opts.Run(c.Context())
		},
	}

	opts.BindFlags(enableProxyCmd)
	cmd.AddCommand(enableProxyCmd)

	return cmd, nil
}
