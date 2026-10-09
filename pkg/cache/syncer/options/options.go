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

package options

import (
	"fmt"

	"github.com/spf13/pflag"
)

// Options holds the configuration flags for the cache-syncer.
type Options struct {
	// InitialPeerURLs seeds the peer list before Cache objects are discovered.
	InitialPeerURLs []string

	// PeerCAFile is the CA cert to verify peer cache-server serving certs.
	PeerCAFile string
	// PeerCertFile is the client cert for authenticating to peers.
	PeerCertFile string
	// PeerKeyFile is the key for the client cert.
	PeerKeyFile string
}

type CompletedOptions struct {
	*Options
}

func NewOptions() *Options {
	return &Options{}
}

func (o *Options) Complete() *CompletedOptions {
	return &CompletedOptions{o}
}

// AddFlags registers flags under the given prefix. Use "" for the standalone binary
// and "cache-syncer-" when embedding inside the cache-server.
func (o *Options) AddFlags(fs *pflag.FlagSet, prefix string) {
	if o == nil {
		return
	}
	fs.StringSliceVar(&o.InitialPeerURLs, prefix+"initial-peer-urls", o.InitialPeerURLs,
		"Comma-separated list of initial peer cache-server URLs to sync to before Cache objects are discovered.")
	fs.StringVar(&o.PeerCAFile, prefix+"peer-ca-file", o.PeerCAFile,
		"CA certificate file for verifying peer cache-server serving certificates.")
	fs.StringVar(&o.PeerCertFile, prefix+"peer-cert-file", o.PeerCertFile,
		"Client certificate file for authenticating to peer cache-servers.")
	fs.StringVar(&o.PeerKeyFile, prefix+"peer-key-file", o.PeerKeyFile,
		"Key file for the peer client certificate.")
}

// Validate returns errors for any missing required TLS fields.
// flagPrefix should match the prefix used in AddFlags so error messages name the right flags.
func (o *Options) Validate(flagPrefix string) []error {
	var errs []error
	if o.PeerCAFile == "" {
		errs = append(errs, fmt.Errorf("--%speer-ca-file is required when the cache-syncer is enabled", flagPrefix))
	}
	if o.PeerCertFile == "" {
		errs = append(errs, fmt.Errorf("--%speer-cert-file is required when the cache-syncer is enabled", flagPrefix))
	}
	if o.PeerKeyFile == "" {
		errs = append(errs, fmt.Errorf("--%speer-key-file is required when the cache-syncer is enabled", flagPrefix))
	}
	return errs
}

func (o *CompletedOptions) Validate(flagPrefix string) []error {
	return o.Options.Validate(flagPrefix)
}
