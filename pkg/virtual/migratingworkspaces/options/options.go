/*
Copyright 2026 The kcp Authors.

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
	"path"

	"github.com/spf13/pflag"

	"k8s.io/client-go/rest"

	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	"github.com/kcp-dev/kcp/pkg/virtual/migratingworkspaces"
	"github.com/kcp-dev/kcp/pkg/virtual/migratingworkspaces/builder"
)

type MigratingWorkspaces struct{}

func New() *MigratingWorkspaces {
	return &MigratingWorkspaces{}
}

func (o *MigratingWorkspaces) AddFlags(flags *pflag.FlagSet, prefix string) {
	if o == nil {
		return
	}
}

func (o *MigratingWorkspaces) Validate(flagPrefix string) []error {
	if o == nil {
		return nil
	}
	return nil
}

func (o *MigratingWorkspaces) NewVirtualWorkspaces(
	rootPathPrefix string,
	config *rest.Config,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	config = rest.AddUserAgent(rest.CopyConfig(config), migratingworkspaces.VirtualWorkspaceName+"-virtual-workspace")
	return builder.BuildVirtualWorkspace(config, path.Join(rootPathPrefix, migratingworkspaces.VirtualWorkspaceName))
}
