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

	"github.com/kcp-dev/cli/pkg/crd/plugin"
)

var (
	crdExample = `
# Convert a CRD in a yaml file to an APIResourceSchema. For a CRD named widgets.example.io, and a prefix value of
# 'today', the new APIResourceSchema's name will be today.widgets.example.io.
%[1]s crd snapshot -f crd.yaml --prefix 2022-05-07 > api-resource-schema.yaml

# Convert a CRD from STDIN
kubectl get crd foo -o yaml | %[1]s crd snapshot -f - --prefix today > output.yaml
`
)

// New provides a command for crd operations.
func New(streams genericclioptions.IOStreams) *cobra.Command {
	cmd := &cobra.Command{
		Use:              "crd",
		Short:            "CRD related operations",
		SilenceUsage:     true,
		TraverseChildren: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	snapshotOptions := plugin.NewSnapshotOptions(streams)

	snapshotCommand := &cobra.Command{
		Use:          "snapshot -f FILE --prefix PREFIX",
		Short:        "Snapshot a CRD and convert it to an APIResourceSchema",
		Example:      fmt.Sprintf(crdExample, "kubectl kcp"),
		SilenceUsage: true,
		RunE: func(c *cobra.Command, args []string) error {
			if err := snapshotOptions.Complete(); err != nil {
				return err
			}

			if err := snapshotOptions.Validate(); err != nil {
				return err
			}

			return snapshotOptions.Run()
		},
	}

	snapshotOptions.BindFlags(snapshotCommand)

	cmd.AddCommand(snapshotCommand)

	return cmd
}
