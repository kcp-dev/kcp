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

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/proxy"
)

func main() {
	flags := pflag.NewFlagSet("kcp-front-proxy", pflag.ExitOnError)
	pflag.CommandLine = flags

	server := proxy.Server{}
	cmd := &cobra.Command{
		Use:   "kcp-front-proxy",
		Short: "Terminate TLS and handles client cert auth for backend API servers",
		Long: `kcp-front-proxy is a reverse proxy that accepts client certificates and
forwards Common Name and Organizations to backend API servers in HTTP headers.
The proxy terminates TLS and communicates with API servers via mTLS. Traffic is
routed based on paths.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return server.Serve()
		},
	}

	// setup klog
	fs := goflags.NewFlagSet("klog", goflags.PanicOnError)
	klog.InitFlags(fs)
	cmd.PersistentFlags().AddGoFlagSet(fs)

	cmd.PersistentFlags().StringVar(&server.ListenAddress, "listen-address", ":8083", "Address and port for the proxy to listen on")
	cmd.PersistentFlags().StringVar(&server.ClientCACert, "client-ca-cert", "certs/kcp-client-ca-cert.pem", "CA cert used to validate client certs")
	cmd.PersistentFlags().StringVar(&server.ServerCertFile, "server-cert-file", "certs/proxy-server-cert.pem", "The proxy's serving cert file")
	cmd.PersistentFlags().StringVar(&server.ServerKeyFile, "server-key-file", "certs/proxy-server-key.pem", "The proxy's serving private key file")
	cmd.PersistentFlags().StringVar(&server.MappingFile, "mapping-file", "", "Config file mapping paths to backends")

	if err := cmd.MarkPersistentFlagRequired("mapping-file"); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
}
