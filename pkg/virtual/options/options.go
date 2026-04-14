/*
Copyright 2022 The kcp Authors.

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

	"github.com/spf13/pflag"

	"k8s.io/client-go/rest"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	apiexportoptions "github.com/kcp-dev/kcp/pkg/virtual/apiexport/options"
	apiresourceschemaoptions "github.com/kcp-dev/kcp/pkg/virtual/apiresourceschema/options"
	initializingworkspacesoptions "github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces/options"
	replicationoptions "github.com/kcp-dev/kcp/pkg/virtual/replication/options"
	terminatingworkspaceoptions "github.com/kcp-dev/kcp/pkg/virtual/terminatingworkspaces/options"
)

const virtualWorkspacesFlagPrefix = "virtual-workspaces-"

type Options struct {
	APIExport              *apiexportoptions.APIExport
	APIResourceSchema      *apiresourceschemaoptions.APIResourceSchema
	InitializingWorkspaces *initializingworkspacesoptions.InitializingWorkspaces
	TerminatingWorkspaces  *terminatingworkspaceoptions.TerminatingWorkspaces
}

func NewOptions() *Options {
	return &Options{
		APIExport:              apiexportoptions.New(),
		APIResourceSchema:      apiresourceschemaoptions.New(),
		InitializingWorkspaces: initializingworkspacesoptions.New(),
		TerminatingWorkspaces:  terminatingworkspaceoptions.New(),
	}
}

func (o *Options) Validate() []error {
	var errs []error //nolint:prealloc

	errs = append(errs, o.APIExport.Validate(virtualWorkspacesFlagPrefix)...)
	errs = append(errs, o.APIResourceSchema.Validate(virtualWorkspacesFlagPrefix)...)
	errs = append(errs, o.InitializingWorkspaces.Validate(virtualWorkspacesFlagPrefix)...)
	errs = append(errs, o.TerminatingWorkspaces.Validate(virtualWorkspacesFlagPrefix)...)

	return errs
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	o.InitializingWorkspaces.AddFlags(fs, virtualWorkspacesFlagPrefix)
	o.TerminatingWorkspaces.AddFlags(fs, virtualWorkspacesFlagPrefix)
	o.APIExport.AddFlags(fs, virtualWorkspacesFlagPrefix)
	o.APIResourceSchema.AddFlags(fs, virtualWorkspacesFlagPrefix)
}

func (o *Options) NewVirtualWorkspaces(
	config *rest.Config,
	cacheConfig *rest.Config,
	rootPathPrefix string,
	wildcardKubeInformers kcpkubernetesinformers.SharedInformerFactory,
	wildcardKcpInformers, cachedKcpInformers kcpinformers.SharedInformerFactory,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	apiexports, err := o.APIExport.NewVirtualWorkspaces(rootPathPrefix, config, cachedKcpInformers, wildcardKcpInformers)
	if err != nil {
		return nil, err
	}

	apiresourceschemas, err := o.APIResourceSchema.NewVirtualWorkspaces(rootPathPrefix, config, cachedKcpInformers, wildcardKcpInformers)
	if err != nil {
		return nil, err
	}

	initializingworkspaces, err := o.InitializingWorkspaces.NewVirtualWorkspaces(rootPathPrefix, config, wildcardKcpInformers)
	if err != nil {
		return nil, err
	}

	replications, err := replicationoptions.New().NewReplication(
		rootPathPrefix,
		config,
		cacheConfig,
		wildcardKcpInformers,
		cachedKcpInformers,
	)
	if err != nil {
		return nil, err
	}

	terminatingworkspaces, err := o.TerminatingWorkspaces.NewVirtualWorkspaces(rootPathPrefix, config, wildcardKcpInformers)
	if err != nil {
		return nil, err
	}

	all, err := Merge(apiexports, apiresourceschemas, initializingworkspaces, replications, terminatingworkspaces)
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
