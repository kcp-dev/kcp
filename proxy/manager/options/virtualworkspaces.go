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
	"strings"

	"github.com/spf13/pflag"
)

// DefaultRootPathPrefix is basically constant forever, or we risk a breaking change. The
// kubectl plugin for example will use this prefix to generate the root path, and because
// we don't control kubectl plugin updates, we cannot change this prefix.
const DefaultRootPathPrefix string = "/services"

type VirtualWorkspaces struct {
	RootPathPrefix   string
	ShardExternalURL string
}

func NewVirtualWorkspaces() *VirtualWorkspaces {
	return &VirtualWorkspaces{
		RootPathPrefix:   DefaultRootPathPrefix,
		ShardExternalURL: "",
	}
}

func (vw *VirtualWorkspaces) AddFlags(flags *pflag.FlagSet) {
	flags.StringVar(&vw.ShardExternalURL, "shard-external-url", vw.ShardExternalURL, "URL used to talk to the virtual workspace of the kcp shard")
	flags.StringVar(&vw.ShardExternalURL, "root-path-prefix", vw.RootPathPrefix, "Prefix used to talk to the virtual workspace of the kcp shard, defaults to services")
}

func (vw *VirtualWorkspaces) Validate() []error {
	errs := []error{}
	if len(vw.ShardExternalURL) == 0 {
		errs = append(errs, fmt.Errorf(("--shard-external-url is required")))
	}
	if !strings.HasPrefix(vw.RootPathPrefix, "/") {
		errs = append(errs, fmt.Errorf("RootPathPrefix %q must start with /", vw.RootPathPrefix))
	}
	return errs
}
