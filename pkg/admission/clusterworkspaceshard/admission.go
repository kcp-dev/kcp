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

package clusterworkspaceshard

import (
	"context"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"

	"github.com/kcp-dev/kcp/pkg/admission/initializers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

// Validate ClusterWorkspace creation and updates for
// - immutability of fields like type
// - valid phase transitions fulfilling pre-conditions
// - status.location.current and status.baseURL cannot be unset.

const (
	PluginName = "tenancy.kcp.dev/ClusterWorkspaceShard"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &clusterWorkspaceShard{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

type clusterWorkspaceShard struct {
	*admission.Handler

	shardBaseURL            string
	shardExternalURL        string
	externalAddressProvider func() string
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&clusterWorkspaceShard{})
var _ = admission.MutationInterface(&clusterWorkspaceShard{})
var _ = initializers.WantsExternalAddressProvider(&clusterWorkspaceShard{})
var _ = initializers.WantsShardExternalURL(&clusterWorkspaceShard{})

// Validate ensures that
// - baseURL is set
// - externalURL is set
func (o *clusterWorkspaceShard) Validate(_ context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("clusterworkspaceshards") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	cws := &tenancyv1alpha1.ClusterWorkspaceShard{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cws); err != nil {
		return fmt.Errorf("failed to convert unstructured to ClusterWorkspace: %w", err)
	}

	var errs field.ErrorList

	if cws.Spec.BaseURL == "" {
		errs = append(errs, field.Required(field.NewPath("spec", "baseURL"), ""))
	}
	if cws.Spec.ExternalURL == "" {
		errs = append(errs, field.Required(field.NewPath("spec", "externalURL"), ""))
	}

	if len(errs) > 0 {
		return admission.NewForbidden(a, errs.ToAggregate())
	}

	return nil
}

// Admit sets.
func (o *clusterWorkspaceShard) Admit(_ context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("clusterworkspaceshards") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	cws := &tenancyv1alpha1.ClusterWorkspaceShard{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cws); err != nil {
		return fmt.Errorf("failed to convert unstructured to ClusterWorkspaceShard: %w", err)
	}

	externalAddress := ""
	if o.externalAddressProvider != nil {
		externalAddress = o.externalAddressProvider()
	}

	if cws.Spec.BaseURL == "" {
		switch {
		case o.shardBaseURL != "":
			cws.Spec.BaseURL = o.shardBaseURL
		case externalAddress != "":
			cws.Spec.BaseURL = "https://" + externalAddress
		}
	}

	if cws.Spec.ExternalURL == "" {
		switch {
		case o.shardExternalURL != "":
			cws.Spec.ExternalURL = o.shardExternalURL
		case externalAddress != "":
			cws.Spec.ExternalURL = "https://" + externalAddress
		}
	}

	if cws.Spec.VirtualWorkspaceURL == "" {
		cws.Spec.VirtualWorkspaceURL = cws.Spec.BaseURL
	}

	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cws)
	if err != nil {
		return err
	}
	u.Object = raw

	return nil
}

func (o *clusterWorkspaceShard) SetShardBaseURL(shardBaseURL string) {
	o.shardBaseURL = shardBaseURL
}

func (o *clusterWorkspaceShard) SetShardExternalURL(shardExternalURL string) {
	o.shardExternalURL = shardExternalURL
}

func (o *clusterWorkspaceShard) SetExternalAddressProvider(externalAddressProvider func() string) {
	o.externalAddressProvider = externalAddressProvider
}
