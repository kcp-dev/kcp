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

	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	apiexportoptions "github.com/kcp-dev/kcp/pkg/virtual/apiexport/options"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	initializingworkspacesoptions "github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces/options"
	synceroptions "github.com/kcp-dev/kcp/pkg/virtual/syncer/options"
	workspacesoptions "github.com/kcp-dev/kcp/pkg/virtual/workspaces/options"
)

const virtualWorkspacesFlagPrefix = "virtual-workspaces-"

type Options struct {
	Workspaces             *workspacesoptions.Workspaces
	Syncer                 *synceroptions.Syncer
	APIExport              *apiexportoptions.APIExport
	InitializingWorkspaces *initializingworkspacesoptions.InitializingWorkspaces
}

func NewOptions() *Options {
	return &Options{
		Workspaces:             workspacesoptions.New(),
		Syncer:                 synceroptions.New(),
		APIExport:              apiexportoptions.New(),
		InitializingWorkspaces: initializingworkspacesoptions.New(),
	}
}

func (v *Options) Validate() []error {
	var errs []error

	errs = append(errs, v.Workspaces.Validate(virtualWorkspacesFlagPrefix)...)
	errs = append(errs, v.Syncer.Validate(virtualWorkspacesFlagPrefix)...)
	errs = append(errs, v.APIExport.Validate(virtualWorkspacesFlagPrefix)...)
	errs = append(errs, v.InitializingWorkspaces.Validate(virtualWorkspacesFlagPrefix)...)

	return errs
}

func (v *Options) AddFlags(fs *pflag.FlagSet) {
	v.Workspaces.AddFlags(fs, virtualWorkspacesFlagPrefix)
	v.InitializingWorkspaces.AddFlags(fs, virtualWorkspacesFlagPrefix)
}

func (o *Options) NewVirtualWorkspaces(
	config *rest.Config,
	rootPathPrefix string,
	wildcardKubeInformers kcpkubernetesinformers.SharedInformerFactory,
	wildcardKcpInformers kcpinformers.SharedInformerFactory,
) ([]rootapiserver.NamedVirtualWorkspace, error) {

	workspaces, err := o.Workspaces.NewVirtualWorkspaces(rootPathPrefix, config)
	if err != nil {
		return nil, err
	}

	syncer, err := o.Syncer.NewVirtualWorkspaces(rootPathPrefix, config, wildcardKcpInformers)
	if err != nil {
		return nil, err
	}

	apiexports, err := o.APIExport.NewVirtualWorkspaces(rootPathPrefix, config, wildcardKcpInformers)
	if err != nil {
		return nil, err
	}

	initializingworkspaces, err := o.InitializingWorkspaces.NewVirtualWorkspaces(rootPathPrefix, config, wildcardKcpInformers)
	if err != nil {
		return nil, err
	}

	all, err := merge(workspaces, syncer, apiexports, initializingworkspaces)
	if err != nil {
		return nil, err
	}
	return all, nil
}

func merge(sets ...[]rootapiserver.NamedVirtualWorkspace) ([]rootapiserver.NamedVirtualWorkspace, error) {
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
