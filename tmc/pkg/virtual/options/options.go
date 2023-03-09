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

	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/options"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
	synceroptions "github.com/kcp-dev/kcp/tmc/pkg/virtual/syncer/options"
)

const virtualWorkspacesFlagPrefix = "virtual-workspaces-"

type Options struct {
	Syncer *synceroptions.Syncer
}

func NewOptions() *Options {
	return &Options{
		Syncer: synceroptions.New(),
	}
}

func (o *Options) Validate() []error {
	var errs []error

	errs = append(errs, o.Syncer.Validate(virtualWorkspacesFlagPrefix)...)

	return errs
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
}

func (o *Options) NewVirtualWorkspaces(
	config *rest.Config,
	rootPathPrefix string,
	shardExternalURL func() string,
	cachedKcpInformers kcpinformers.SharedInformerFactory,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	syncer, err := o.Syncer.NewVirtualWorkspaces(rootPathPrefix, shardExternalURL, config, cachedKcpInformers)
	if err != nil {
		return nil, err
	}

	all, err := options.Merge(syncer)
	if err != nil {
		return nil, err
	}
	return all, nil
}
