/*
Copyright 2025 The kcp Authors.

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
	"os"

	"github.com/spf13/cobra"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/cli"

	"github.com/kcp-dev/sdk/cmd/help"

	synceroptions "github.com/kcp-dev/kcp/pkg/cache/syncer/options"
)

func main() {
	opts := synceroptions.NewOptions()
	cmd := &cobra.Command{
		Use:   "cache-syncer",
		Short: "Syncs resources from a source cache-server to peer cache-servers",
		Long: help.Doc(`
			Connects to a source cache-server and replicates annotated resources to
			peer cache-servers. The source is resolved from the environment (in-cluster
			config, KUBECONFIG env var, or ~/.kube/config). Peer credentials are
			provided via --peer-ca-file, --peer-cert-file, and --peer-key-file.
		`),

		RunE: func(c *cobra.Command, args []string) error {
			completed := opts.Complete()
			if errs := completed.Validate(""); len(errs) > 0 {
				return utilerrors.NewAggregate(errs)
			}

			_, err := clientcmd.BuildConfigFromFlags("", "")
			if err != nil {
				return err
			}

			ctx := genericapiserver.SetupSignalContext()

			// TODO: create infomers, run the controllers...

			<-ctx.Done()
			return nil
		},
	}

	opts.AddFlags(cmd.Flags(), "") // "" prefix → unprefixed flags for standalone use
	code := cli.Run(cmd)
	os.Exit(code)
}
