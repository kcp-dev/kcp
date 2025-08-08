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

package validatingwebhook

import (
	"context"
	"errors"
	"fmt"
	"io"

	"k8s.io/apiserver/pkg/admission"

	vwinitializers "github.com/kcp-dev/kcp/pkg/virtual/framework/admission/initializers"
	virtualcontext "github.com/kcp-dev/kcp/pkg/virtual/framework/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
)

const (
	PluginName = "apis.kcp.io/VWValidatingWebhook"
)

type Plugin struct {
	*admission.Handler

	virtualWorkspaces func() []rootapiserver.NamedVirtualWorkspace
}

var (
	_ = admission.ValidationInterface(&Plugin{})
	_ = admission.InitializationValidator(&Plugin{})
	_ = vwinitializers.WantsVirtualWorkspaces(&Plugin{})
)

func NewValidatingAdmissionWebhook() (*Plugin, error) {
	return &Plugin{
		Handler: admission.NewHandler(admission.Connect, admission.Create, admission.Delete, admission.Update),
	}, nil
}

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(configFile io.Reader) (admission.Interface, error) {
		return NewValidatingAdmissionWebhook()
	})
}

func (p *Plugin) Validate(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) (err error) {
	if err := p.ValidateInitialization(); err != nil {
		return fmt.Errorf("error validating ValidatingWebhook initialization: %w", err)
	}

	virtualWorkspaceName, _ := virtualcontext.VirtualWorkspaceNameFrom(ctx)
	if virtualWorkspaceName == "" {
		// TODO(xmudrii): decide if we want an error or nil.
		return nil
	}

	for _, vw := range p.virtualWorkspaces() {
		// TODO(xmudrii): decide if we want to allow func to be nil.
		if vw.Name == virtualWorkspaceName {
			if vw.Name != "apiexport" {
				continue
			}
			return vw.VirtualWorkspace.Validate(ctx, a, o)
		}
	}

	// TODO(xmudrii): decide if we want an error or nil.
	return nil
}

func (p *Plugin) ValidateInitialization() error {
	if p.virtualWorkspaces == nil {
		return errors.New("missing virtualWorkspaces")
	}
	return nil
}

func (p *Plugin) SetVirutalWorkspaces(virtualWorkspaces func() []rootapiserver.NamedVirtualWorkspace) {
	p.virtualWorkspaces = virtualWorkspaces
}
