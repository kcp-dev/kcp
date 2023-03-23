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
	"github.com/kcp-dev/client-go/kubernetes"
	"github.com/spf13/pflag"

	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/clientsethack"
	"k8s.io/apiserver/pkg/informerfactoryhack"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericapiserveroptions "k8s.io/apiserver/pkg/server/options"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/rest"

	kcpadmissioninitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	virtualadmission "github.com/kcp-dev/kcp/pkg/virtual/apiexport/admission"
	apiexportoptions "github.com/kcp-dev/kcp/pkg/virtual/apiexport/options"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	initializingworkspacesoptions "github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces/options"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

const virtualWorkspacesFlagPrefix = "virtual-workspaces-"

type Options struct {
	APIExport              *apiexportoptions.APIExport
	InitializingWorkspaces *initializingworkspacesoptions.InitializingWorkspaces
	Admission              *genericapiserveroptions.AdmissionOptions
}

func NewOptions() *Options {
	o := &Options{
		APIExport:              apiexportoptions.New(),
		InitializingWorkspaces: initializingworkspacesoptions.New(),
		Admission:              genericapiserveroptions.NewAdmissionOptions(),
	}

	// plugins that kube will always add (NamespaceLifecycle, MutatingWebHook, ValidatingWebhook)
	defaultPlugins := o.Admission.Plugins.Registered()

	// Enable admission here so that it is available to both kcp and the virtual-workspaces server
	virtualadmission.Register(o.Admission.Plugins)
	o.Admission.RecommendedPluginOrder = []string{
		virtualadmission.PluginName,
	}

	// Turn off the default admission plugins, since they register automatically.
	// We must list the default plugins in the recommended order because they are registered
	o.Admission.RecommendedPluginOrder = append(o.Admission.RecommendedPluginOrder, defaultPlugins...)

	// Disabled plugins that are not in RecommendedPluginOrder will fail validation.
	//  kcp's DefaultOffAdmissionPlugins list includes _all_ kubernetes plugins.
	o.Admission.DisablePlugins = defaultPlugins

	return o
}

func (o *Options) Validate() []error {
	var errs []error

	errs = append(errs, o.APIExport.Validate(virtualWorkspacesFlagPrefix)...)
	errs = append(errs, o.InitializingWorkspaces.Validate(virtualWorkspacesFlagPrefix)...)
	errs = append(errs, o.Admission.Validate()...)

	return errs
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	o.InitializingWorkspaces.AddFlags(fs, virtualWorkspacesFlagPrefix)
}

func (o *Options) NewVirtualWorkspaces(
	rootConfig *genericapiserver.RecommendedConfig,
	config *rest.Config,
	rootPathPrefix string,
	wildcardKubeInformers kcpkubernetesinformers.SharedInformerFactory,
	wildcardKcpInformers, cachedKcpInformers kcpinformers.SharedInformerFactory,
	localShardKubeClusterClient kubernetes.ClusterInterface,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	apiexports, err := o.APIExport.NewVirtualWorkspaces(rootPathPrefix, config, cachedKcpInformers)
	if err != nil {
		return nil, err
	}

	// apply admission to check permission claims for resources through a view's URL
	admissionPluginInitializers := []admission.PluginInitializer{
		kcpadmissioninitializers.NewKcpInformersInitializer(wildcardKcpInformers, cachedKcpInformers),
	}
	err = o.Admission.ApplyTo(
		&rootConfig.Config,
		informerfactoryhack.Wrap(wildcardKubeInformers),
		clientsethack.Wrap(localShardKubeClusterClient),
		utilfeature.DefaultFeatureGate,
		admissionPluginInitializers...,
	)
	if err != nil {
		return nil, err
	}

	initializingworkspaces, err := o.InitializingWorkspaces.NewVirtualWorkspaces(rootPathPrefix, config, wildcardKcpInformers)
	if err != nil {
		return nil, err
	}

	all, err := Merge(apiexports, initializingworkspaces)
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
