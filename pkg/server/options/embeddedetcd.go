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

	"github.com/spf13/pflag"
)

type EmbeddedEtcd struct {
	Enabled bool

	Directory    string
	PeerPort     string
	ClientPort   string
	WalSizeBytes int64
}

func NewEmbeddedEtcd() *EmbeddedEtcd {
	return &EmbeddedEtcd{
		Directory:  "etcd-server",
		PeerPort:   "2380",
		ClientPort: "2379",
	}
}

func (e *EmbeddedEtcd) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&e.Directory, "embedded-etcd-directory", e.Directory, "Directory for embedded etcd")
	fs.StringVar(&e.PeerPort, "embedded-etcd-peer-port", e.PeerPort, "Port for embedded etcd peer")
	fs.StringVar(&e.ClientPort, "embedded-etcd-client-port", e.ClientPort, "Port for embedded etcd client")
	fs.Int64Var(&e.WalSizeBytes, "embedded-etcd-wal-size-bytes", e.WalSizeBytes, "Size of embedded etcd WAL")
}

func (e *EmbeddedEtcd) Validate() []error {
	var errs []error

	if e.Enabled {
		if e.PeerPort == "" {
			errs = append(errs, fmt.Errorf("--embedded-etcd-peer-port must be specified"))
		}
		if e.ClientPort == "" {
			errs = append(errs, fmt.Errorf("--embedded-etcd-client-port must be specified"))
		}
	}

	return errs
}
