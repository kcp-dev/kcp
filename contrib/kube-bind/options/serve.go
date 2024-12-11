/*
Copyright 2022 The Kube Bind Authors.

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

package options

import (
	"fmt"
	"net"

	"github.com/spf13/pflag"
)

type Serve struct {
	ListenIP          string
	ListenPort        int
	ListenAddress     string
	CertFile, KeyFile string

	// Listener is used to pre-wire a port zero listener for testing.
	Listener net.Listener
}

func NewServe() *Serve {
	return &Serve{
		ListenAddress: "127.0.0.1:8080",
	}
}

func (options *Serve) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&options.ListenIP, "listen-ip", options.ListenIP, "The host IP where the backend is running")
	fs.MarkDeprecated("listen-ip", "Use listen-address instead") // nolint: errcheck
	fs.IntVar(&options.ListenPort, "listen-port", options.ListenPort, "The host port where the backend is running")
	fs.MarkDeprecated("listen-port", "Use listen-address instead") // nolint: errcheck
	fs.StringVar(&options.ListenAddress, "listen-address", options.ListenAddress, "The address where the backend should be listening on, defaults to 127.0.0.1:8080.")
	fs.StringVar(&options.CertFile, "tls-cert-file", options.CertFile, "The TLS certificate file the webserver will use.")
	fs.StringVar(&options.KeyFile, "tls-key-file", options.KeyFile, "The TLS private key file the webserver will use.")
}

func (options *Serve) Complete() error {
	return nil
}

func (options *Serve) Validate() error {
	if (options.ListenIP == "") != (options.ListenAddress == "") {
		return fmt.Errorf("either listen-ip or listen-address must be provided")
	}
	if options.CertFile == "" && options.KeyFile != "" {
		return fmt.Errorf("TLS key file cannot be specified without TLS cert file")
	}
	if options.CertFile != "" && options.KeyFile == "" {
		return fmt.Errorf("TLS cert file cannot be specified without TLS key file")
	}

	return nil
}
