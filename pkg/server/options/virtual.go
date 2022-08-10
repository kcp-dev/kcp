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
	"github.com/spf13/pflag"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/pkg/virtual/options"
)

type Virtual struct {
	VirtualWorkspaces virtualworkspacesoptions.Options
	Enabled           bool
}

func NewVirtual() *Virtual {
	return &Virtual{
		VirtualWorkspaces: *virtualworkspacesoptions.NewOptions(),

		Enabled: true,
	}
}

func (v *Virtual) Validate() []error {
	var errs []error

	if v.Enabled {
		errs = append(errs, v.VirtualWorkspaces.Validate()...)
	}

	return errs
}

func (v *Virtual) AddFlags(fs *pflag.FlagSet) {
	v.VirtualWorkspaces.AddFlags(fs)

	fs.BoolVar(&v.Enabled, "run-virtual-workspaces", v.Enabled, "Run the virtual workspace apiservers in-process")
}
