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
	"net/url"

	"github.com/spf13/pflag"

	rootoptions "github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver/options"
)

type Virtual struct {
	Root    rootoptions.Root
	Enabled bool

	// ExternalVirtualWorkspaceAddress holds a URL to redirect to for stand-alone virtual workspaces.
	ExternalVirtualWorkspaceAddress string
}

func NewVirtual() *Virtual {
	return &Virtual{
		Root: *rootoptions.NewRoot(),

		Enabled: true,
	}
}

func (v *Virtual) Validate() []error {
	var errs []error

	if v.Enabled {
		errs = append(errs, v.Root.Validate()...)

		if v.ExternalVirtualWorkspaceAddress != "" {
			errs = append(errs, fmt.Errorf("--virtual-workspace-address must be empty if virtual workspaces run in-process"))
		}
	} else {
		if v.ExternalVirtualWorkspaceAddress == "" {
			errs = append(errs, fmt.Errorf("--virtual-workspace-address is required if virtual workspaces run out-of-process"))
		} else if u, err := url.Parse(v.ExternalVirtualWorkspaceAddress); err != nil {
			errs = append(errs, fmt.Errorf("--virtual-workspace-address must be a valid URL: %w", err))
		} else if u.Scheme != "http" && u.Scheme != "https" {
			errs = append(errs, fmt.Errorf("--virtual-workspace-address must be a valid  https URL"))
		}
	}

	return errs
}

func (v *Virtual) AddFlags(fs *pflag.FlagSet) {
	v.Root.AddFlags(fs)

	fs.BoolVar(&v.Enabled, "run-virtual-workspaces", v.Enabled, "Run the virtual workspace apiservers in-process")
	fs.StringVar(&v.ExternalVirtualWorkspaceAddress, "virtual-workspace-address", v.ExternalVirtualWorkspaceAddress, "Address of a stand-alone virtual workspace apiserver (without the /services path)")
}
