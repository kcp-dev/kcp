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
	"context"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/spf13/cobra"

	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/pkg/cmd/help"
	crdpuller "github.com/kcp-dev/kcp/pkg/crdpuller"
)

func main() {
	cmd := &cobra.Command{
		Use:        "pull-crds",
		Aliases:    []string{},
		SuggestFor: []string{},
		Short:      "Pull CRDs from a Kubernetes cluster",
		Long: help.Doc(`
					Pull CRDs from a Kubernetes cluster
					Based on a kubeconfig file, it uses discovery API and the OpenAPI v2
					model on the cluster to build CRDs for a list of api resource names.
				`),
		Example: "",
		RunE: func(cmd *cobra.Command, args []string) error {
			kubeconfigPath := cmd.Flag("kubeconfig").Value.String()
			config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
			if err != nil {
				return err
			}
			puller, err := crdpuller.NewSchemaPuller(config)
			if err != nil {
				return err
			}
			crds, err := puller.PullCRDs(context.TODO(), args...)
			if err != nil {
				return err
			}
			for name, crd := range crds {
				yamlBytes, err := yaml.Marshal(crd)
				if err != nil {
					return err
				}
				if err := ioutil.WriteFile(name.String()+".yaml", yamlBytes, os.ModePerm); err != nil {
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().String("kubeconfig", ".kubeconfig", "kubeconfig file used to contact the cluster.")

	help.FitTerminal(cmd.OutOrStdout())

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
}
