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

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	"github.com/spf13/pflag"

	"k8s.io/client-go/rest"

	apiexportoptions "github.com/kcp-dev/kcp/pkg/virtual/apiexport/options"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	initializingworkspacesoptions "github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces/options"
	proxyinformers "github.com/kcp-dev/kcp/proxy/client/informers/externalversions"
	proxyoptions "github.com/kcp-dev/kcp/proxy/virtual/proxy/options"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

const virtualWorkspacesFlagPrefix = "virtual-workspaces-"

type Options struct {
	APIExport              *apiexportoptions.APIExport
	InitializingWorkspaces *initializingworkspacesoptions.InitializingWorkspaces
	ProxyVirtualWorkspaces *proxyoptions.Proxy
}

func NewOptions() *Options {
	return &Options{
		APIExport:              apiexportoptions.New(),
		InitializingWorkspaces: initializingworkspacesoptions.New(),
		ProxyVirtualWorkspaces: proxyoptions.New(),
	}
}

func (o *Options) Validate() []error {
	var errs []error

	errs = append(errs, o.APIExport.Validate(virtualWorkspacesFlagPrefix)...)
	errs = append(errs, o.InitializingWorkspaces.Validate(virtualWorkspacesFlagPrefix)...)
	errs = append(errs, o.ProxyVirtualWorkspaces.Validate(virtualWorkspacesFlagPrefix)...)

	return errs
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	o.InitializingWorkspaces.AddFlags(fs, virtualWorkspacesFlagPrefix)
	o.ProxyVirtualWorkspaces.AddFlags(fs, virtualWorkspacesFlagPrefix)
}

func (o *Options) NewVirtualWorkspaces(
	config *rest.Config,
	rootPathPrefix string,
	shardExternalURL func() string,
	wildcardKubeInformers kcpkubernetesinformers.SharedInformerFactory,
	wildcardKcpInformers, cachedKcpInformers kcpinformers.SharedInformerFactory,
	cachedProxyInformers proxyinformers.SharedInformerFactory,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	apiexports, err := o.APIExport.NewVirtualWorkspaces(rootPathPrefix, config, cachedKcpInformers)
	if err != nil {
		return nil, err
	}

	initializingworkspaces, err := o.InitializingWorkspaces.NewVirtualWorkspaces(rootPathPrefix, config, wildcardKcpInformers)
	if err != nil {
		return nil, err
	}

	// Move to a separate binary
	// create proxy builder
	// TODO: move to manager
	proxyWorkspace, err := o.ProxyVirtualWorkspaces.NewVirtualWorkspaces(rootPathPrefix, config, cachedProxyInformers)
	if err != nil {
		return nil, err
	}

	all, err := Merge(apiexports, initializingworkspaces, proxyWorkspace)
	if err != nil {
		return nil, err
	}
	return all, nil
}

func Merge(sets ...[]rootapiserver.NamedVirtualWorkspace) ([]rootapiserver.NamedVirtualWorkspace, error) {
	var workspaces []rootapiserver.NamedVirtualWorkspace
	seen := map[string]bool{}
	for _, set := range sets {
		for _, vw := range set {
			if seen[vw.Name] {
				return nil, fmt.Errorf("duplicate virtual workspace %q", vw.Name)
			}
			seen[vw.Name] = true
		}
		workspaces = append(workspaces, set...)
	}
	return workspaces, nil
}
