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

package initializers

import (
	"k8s.io/apiserver/pkg/admission"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
)

// NewVirtualWorkspacesInitializer returns an admission plugin initializer that injects
// both virtual workspaces factories into admission plugins.
func NewVirtualWorkspacesInitializer(
	virtualWorkspaces func() []rootapiserver.NamedVirtualWorkspace,
) *virtualWorkspacesInitializer {
	return &virtualWorkspacesInitializer{
		virtualWorkspaces: virtualWorkspaces,
	}
}

type virtualWorkspacesInitializer struct {
	virtualWorkspaces func() []rootapiserver.NamedVirtualWorkspace
}

func (i *virtualWorkspacesInitializer) Initialize(plugin admission.Interface) {
	if wants, ok := plugin.(WantsVirtualWorkspaces); ok {
		wants.SetVirtualWorkspaces(i.virtualWorkspaces)
	}
}
