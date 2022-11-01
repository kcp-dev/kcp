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
	"github.com/spf13/cobra"

	synceroptions "github.com/kcp-dev/kcp/cmd/syncer/options"
	"github.com/kcp-dev/kcp/pkg/dns/plugin/nsmap"
	"github.com/kcp-dev/kcp/third_party/coredns/coremain"
)

func NewDNSCommand() *cobra.Command {
	options := synceroptions.NewDNSOptions()
	dnsCommand := &cobra.Command{
		Use:   "dns",
		Short: "Manage kcp dns server",
	}

	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the kcp dns server",

		RunE: func(cmd *cobra.Command, args []string) error {
			if err := options.Complete(); err != nil {
				return err
			}
			if err := options.Validate(); err != nil {
				return err
			}

			nsmap.ConfigMapName = options.ConfigMapName

			coremain.Start()
			return nil
		},
	}
	options.AddFlags(startCmd.Flags())

	dnsCommand.AddCommand(startCmd)

	return dnsCommand
}
