/*
Copyright 2023 The KCP Authors.

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

package admission

import (
	"context"
	"errors"
	"fmt"
	"io"

	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/kcp/pkg/permissionclaim"
	virtualcontext "github.com/kcp-dev/kcp/pkg/virtual/framework/context"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

const (
	PluginName = "apis.view.kcp.io/PermissionClaims"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(configFile io.Reader) (admission.Interface, error) {
		return NewPermissionClaimAdmitter(), nil
	})
}

type permissionClaimAdmitter struct {
	*admission.Handler

	apiBindingsHasSynced cache.InformerSynced

	permissionClaimLabeler *permissionclaim.Labeler
}

var _ admission.ValidationInterface = &permissionClaimAdmitter{}
var _ admission.InitializationValidator = &permissionClaimAdmitter{}

// NewPermissionClaimAdmitter creates a validating admission plugin that is responsible for admitting objects
// according to permission claims. On every creation and update request, we will determine the bindings
// in the workspace and if the object is claimed by an accepted permission claim we will allow it.
func NewPermissionClaimAdmitter() *permissionClaimAdmitter {
	p := &permissionClaimAdmitter{
		Handler: admission.NewHandler(admission.Create, admission.Update),
	}

	p.SetReadyFunc(
		func() bool {
			return p.apiBindingsHasSynced()
		},
	)

	return p
}

func (m *permissionClaimAdmitter) Validate(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) error {
	if _, hasVirtualWorkspaceName := virtualcontext.VirtualWorkspaceNameFrom(ctx); !hasVirtualWorkspaceName {
		// The request is not for a virtual workspace, so we can safely admit.
		return nil
	}

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return err
	}

	permitted, err := m.permissionClaimLabeler.ObjectPermitted(ctx, clusterName, a.GetResource().GroupResource(), a.GetNamespace(), a.GetName())
	if err != nil {
		return admission.NewForbidden(a, err)
	}
	if !permitted {
		return admission.NewForbidden(a, fmt.Errorf("operation not permitted"))
	}

	return nil
}

// SetKcpInformers implements the WantsExternalKcpInformerFactory interface.
func (m *permissionClaimAdmitter) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	m.apiBindingsHasSynced = func() bool {
		return local.Apis().V1alpha1().APIBindings().Informer().HasSynced() &&
			local.Apis().V1alpha1().APIExports().Informer().HasSynced() &&
			global.Apis().V1alpha1().APIExports().Informer().HasSynced()
	}

	m.permissionClaimLabeler = permissionclaim.NewLabeler(
		local.Apis().V1alpha1().APIBindings(),
		local.Apis().V1alpha1().APIExports(),
		global.Apis().V1alpha1().APIExports(),
	)
}

func (m *permissionClaimAdmitter) ValidateInitialization() error {
	if m.apiBindingsHasSynced == nil {
		return errors.New("missing apiBindingsHasSynced")
	}
	if m.permissionClaimLabeler == nil {
		return errors.New("missing permissionClaimLabeler")
	}
	return nil
}
