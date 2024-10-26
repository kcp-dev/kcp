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

package options

import (
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/options"
	"github.com/spf13/pflag"

	"k8s.io/client-go/rest"

	proxyinformers "github.com/kcp-dev/kcp/contrib/mounts-vw/client/informers/externalversions"
	proxyoptions "github.com/kcp-dev/kcp/contrib/mounts-vw/virtual/proxy/options"
)

const virtualWorkspacesFlagPrefix = "virtual-workspaces-"

type Options struct {
	Proxy *proxyoptions.Proxy
}

func NewOptions() *Options {
	return &Options{
		Proxy: proxyoptions.New(),
	}
}

func (o *Options) Validate() []error {
	var errs []error

	errs = append(errs, o.Proxy.Validate(virtualWorkspacesFlagPrefix)...)

	return errs
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
}

func (o *Options) NewVirtualWorkspaces(
	rootPathPrefix string,
	config *rest.Config,
	cachedProxyInformers proxyinformers.SharedInformerFactory,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	proxy, err := o.Proxy.NewVirtualWorkspaces(rootPathPrefix, config, cachedProxyInformers)
	if err != nil {
		return nil, err
	}

	all, err := options.Merge(proxy)
	if err != nil {
		return nil, err
	}
	return all, nil
}
