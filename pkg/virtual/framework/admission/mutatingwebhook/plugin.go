/*
Copyright 2025 The KCP Authors.

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

package mutatingwebhook

import (
	"context"
	"errors"
	"fmt"
	"io"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/admission"

	vwinitializers "github.com/kcp-dev/kcp/pkg/virtual/framework/admission/initializers"
	virtualcontext "github.com/kcp-dev/kcp/pkg/virtual/framework/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
)

const (
	PluginName = "apis.kcp.io/VWMutatingWebhook"
)

type Plugin struct {
	*admission.Handler

	virtualWorkspaces func() []rootapiserver.NamedVirtualWorkspace
}

var (
	_ = admission.MutationInterface(&Plugin{})
	_ = admission.InitializationValidator(&Plugin{})
	_ = vwinitializers.WantsVirtualWorkspaces(&Plugin{})
)

func NewMutatingAdmissionWebhook() (*Plugin, error) {
	return &Plugin{
		// We're handling all possible operations, it's up to the plugin implemention to decide which one to
		// handle.
		Handler: admission.NewHandler(admission.Connect, admission.Create, admission.Delete, admission.Update),
	}, nil
}

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(_ io.Reader) (admission.Interface, error) {
		return NewMutatingAdmissionWebhook()
	})
}

func (p *Plugin) Admit(ctx context.Context, attr admission.Attributes, o admission.ObjectInterfaces) error {
	if err := p.ValidateInitialization(); err != nil {
		return kerrors.NewInternalError(fmt.Errorf("error validating MutatingWebhook initialization: %w", err))
	}

	virtualWorkspaceName, _ := virtualcontext.VirtualWorkspaceNameFrom(ctx)
	if virtualWorkspaceName == "" {
		return kerrors.NewBadRequest("path not resolved to a valid virtual workspace")
	}

	for _, vw := range p.virtualWorkspaces() {
		if vw.Name == virtualWorkspaceName {
			return vw.VirtualWorkspace.Admit(ctx, attr, o)
		}
	}

	return nil
}

func (p *Plugin) ValidateInitialization() error {
	if p.virtualWorkspaces == nil {
		return errors.New("missing virtualWorkspaces")
	}
	return nil
}

func (p *Plugin) SetVirtualWorkspaces(virtualWorkspaces func() []rootapiserver.NamedVirtualWorkspace) {
	p.virtualWorkspaces = virtualWorkspaces
}
