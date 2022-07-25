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

package options

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/pflag"
	etcdtypes "go.etcd.io/etcd/client/pkg/v3/types"

	genericoptions "k8s.io/apiserver/pkg/server/options"
)

type Options struct {
	Enabled bool

	Directory         string
	PeerPort          string
	ClientPort        string
	ListenMetricsURLs []string
	WalSizeBytes      int64
	QuotaBackendBytes int64
	ForceNewCluster   bool
}

func NewOptions(rootDir string) *Options {
	return &Options{
		Directory:  filepath.Join(rootDir, "etcd-server"),
		PeerPort:   "2380",
		ClientPort: "2379",
	}
}

func (e *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&e.Directory, "embedded-etcd-directory", e.Directory, "Directory for embedded etcd")
	fs.StringVar(&e.PeerPort, "embedded-etcd-peer-port", e.PeerPort, "Port for embedded etcd peer")
	fs.StringVar(&e.ClientPort, "embedded-etcd-client-port", e.ClientPort, "Port for embedded etcd client")
	fs.StringSliceVar(&e.ListenMetricsURLs, "embedded-etcd-listen-metrics-urls", e.ListenMetricsURLs, "The list of protocol://host:port where embedded etcd server listens for Prometheus scrapes")
	fs.Int64Var(&e.WalSizeBytes, "embedded-etcd-wal-size-bytes", e.WalSizeBytes, "Size of embedded etcd WAL")
	fs.Int64Var(&e.QuotaBackendBytes, "embedded-etcd-quota-backend-bytes", e.WalSizeBytes, "Alarm threshold for embedded etcd backend bytes")
	fs.BoolVar(&e.ForceNewCluster, "embedded-etcd-force-new-cluster", e.ForceNewCluster, "Starts a new cluster from existing data restored from a different system")
}

type completedOptions struct {
	*Options
}

type CompletedOptions struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedOptions
}

func (e *Options) Complete(etcdOptions *genericoptions.EtcdOptions) CompletedOptions {
	if e.Enabled {
		etcdOptions.StorageConfig.Transport.ServerList = []string{fmt.Sprintf("https://localhost:%s", e.ClientPort)}
		etcdOptions.StorageConfig.Transport.KeyFile = filepath.Join(e.Directory, "secrets", "client", "key.pem")
		etcdOptions.StorageConfig.Transport.CertFile = filepath.Join(e.Directory, "secrets", "client", "cert.pem")
		etcdOptions.StorageConfig.Transport.TrustedCAFile = filepath.Join(e.Directory, "secrets", "ca", "cert.pem")
	}

	return CompletedOptions{&completedOptions{
		Options: e,
	}}
}

func (e *Options) Validate() []error {
	var errs []error

	if e.Enabled {
		if e.PeerPort == "" {
			errs = append(errs, fmt.Errorf("--embedded-etcd-peer-port must be specified"))
		}
		if e.ClientPort == "" {
			errs = append(errs, fmt.Errorf("--embedded-etcd-client-port must be specified"))
		}
		if len(e.ListenMetricsURLs) > 0 {
			_, err := etcdtypes.NewURLs(e.ListenMetricsURLs)
			if err != nil {
				errs = append(errs, fmt.Errorf("--embedded-etcd-listen-metrics-urls parse failure: %w", err))
			}
		}
	}

	return errs
}
