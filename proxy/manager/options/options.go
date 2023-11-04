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
	cliflag "k8s.io/component-base/cli/flag"

	cacheclientoptions "github.com/kcp-dev/kcp/proxy/cache/client/options"
	proxyvirtualoptions "github.com/kcp-dev/kcp/proxy/virtual/options"
)

type Options struct {
	ProxyControllers       Controllers
	VirtualWorkspaces      VirtualWorkspaces
	ProxyVirtualWorkspaces proxyvirtualoptions.Options
	CacheClient            cacheclientoptions.Cache

	Extra ExtraOptions
}

type ExtraOptions struct {
}

type completedOptions struct {
	Controllers            Controllers
	VirtualWorkspaces      VirtualWorkspaces
	ProxyVirtualWorkspaces proxyvirtualoptions.Options
	CacheClient            cacheclientoptions.Cache

	Extra ExtraOptions
}

type CompletedOptions struct {
	*completedOptions
}

// NewOptions creates a new Options with default parameters.
func NewOptions() *Options {
	o := &Options{
		ProxyControllers:       *NewProxyControllers(),
		VirtualWorkspaces:      *NewVirtualWorkspaces(),           // Options to connect to the virtual workspaces APIs of the kcp cluster
		ProxyVirtualWorkspaces: *proxyvirtualoptions.NewOptions(), // Options to create Proxy specific virtual workspaces
		CacheClient:            *cacheclientoptions.NewCache(),

		Extra: ExtraOptions{},
	}
	// add TMC admission plugins
	// TODO (FGI): this will need to be replaced by off process admission webhooks
	// tmcadmission.RegisterAllTMCAdmissionPlugins(o.Core.GenericControlPlane.Admission.Plugins)
	return o
}

func (o *Options) AddFlags(fss *cliflag.NamedFlagSets) {
	o.ProxyControllers.AddFlags(fss.FlagSet("Proxy Controllers"))
	o.VirtualWorkspaces.AddFlags(fss.FlagSet("KCP Virtual workspaces APIs"))
	o.ProxyVirtualWorkspaces.AddFlags(fss.FlagSet("Proxy Virtual Workspaces"))
	o.CacheClient.AddFlags(fss.FlagSet("Cache Client"))
}

func (o *CompletedOptions) Validate() []error {
	var errs []error
	errs = append(errs, o.Controllers.Validate()...)
	errs = append(errs, o.VirtualWorkspaces.Validate()...)
	return errs
}

func (o *Options) Complete() (*CompletedOptions, error) {
	if err := o.ProxyControllers.Complete(); err != nil {
		return nil, err
	}

	return &CompletedOptions{
		completedOptions: &completedOptions{
			Controllers:            o.ProxyControllers,
			VirtualWorkspaces:      o.VirtualWorkspaces,
			ProxyVirtualWorkspaces: o.ProxyVirtualWorkspaces,
			Extra:                  o.Extra,
			CacheClient:            o.CacheClient,
		},
	}, nil
}
