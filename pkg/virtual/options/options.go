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

	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/client-go/dynamic"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	apiexportoptions "github.com/kcp-dev/kcp/pkg/virtual/apiexport/options"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
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
		Workspaces:             workspacesoptions.NewWorkspaces(),
		Syncer:                 synceroptions.NewSyncer(),
		APIExport:              apiexportoptions.NewAPIExport(),
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
	rootPathPrefix string,
	kubeClusterClient kubernetes.ClusterInterface,
	dynamicClusterClient dynamic.ClusterInterface,
	kcpClusterClient kcpclient.ClusterInterface,
	wildcardKubeInformers kubeinformers.SharedInformerFactory,
	wildcardApiExtensionsInformers apiextensionsinformers.SharedInformerFactory,
	wildcardKcpInformers kcpinformer.SharedInformerFactory,
) (extraInformers []rootapiserver.InformerStart, workspaces []framework.VirtualWorkspace, err error) {

	inf, vws, err := o.Workspaces.NewVirtualWorkspaces(rootPathPrefix, kubeClusterClient, dynamicClusterClient, kcpClusterClient, wildcardKubeInformers, wildcardKcpInformers)
	if err != nil {
		return nil, nil, err
	}
	extraInformers = append(extraInformers, inf...)
	workspaces = append(workspaces, vws...)

	inf, vws, err = o.Syncer.NewVirtualWorkspaces(rootPathPrefix, kubeClusterClient, dynamicClusterClient, kcpClusterClient, wildcardKcpInformers)
	if err != nil {
		return nil, nil, err
	}
	extraInformers = append(extraInformers, inf...)
	workspaces = append(workspaces, vws...)

	inf, vws, err = o.APIExport.NewVirtualWorkspaces(rootPathPrefix, dynamicClusterClient, kcpClusterClient, wildcardKcpInformers)
	if err != nil {
		return nil, nil, err
	}
	extraInformers = append(extraInformers, inf...)
	workspaces = append(workspaces, vws...)

	inf, vws, err = o.InitializingWorkspaces.NewVirtualWorkspaces(rootPathPrefix, dynamicClusterClient, wildcardApiExtensionsInformers)
	if err != nil {
		return nil, nil, err
	}
	extraInformers = append(extraInformers, inf...)
	workspaces = append(workspaces, vws...)

	return extraInformers, workspaces, nil
}
