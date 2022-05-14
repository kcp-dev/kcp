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

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	workspacesoptions "github.com/kcp-dev/kcp/pkg/virtual/workspaces/options"
)

const virtualWorkspacesFlagPrefix = "virtual-workspaces-"

type Options struct {
	Workspaces *workspacesoptions.Workspaces
}

func NewOptions() *Options {
	return &Options{
		Workspaces: workspacesoptions.NewWorkspaces(),
	}
}

// TODO: possibly add the prefix back here (for nicer stuff on the vw standalone commandline)
// and move the constant to the server package
func (v *Options) Validate() []error {
	var errs []error

	errs = append(errs, v.Workspaces.Validate(virtualWorkspacesFlagPrefix)...)

	return errs
}

// TODO: possibly add the prefix back here (for nicer stuff on the vw standalone commandline)
func (v *Options) AddFlags(fs *pflag.FlagSet) {
	v.Workspaces.AddFlags(fs, virtualWorkspacesFlagPrefix)
}

func (o *Options) NewVirtualWorkspaces(
	rootPathPrefix string,
	kubeClusterClient kubernetes.ClusterInterface,
	dynamicClusterClient dynamic.ClusterInterface,
	kcpClusterClient kcpclient.ClusterInterface,
	wildcardKubeInformers informers.SharedInformerFactory,
	wildcardKcpInformers kcpinformer.SharedInformerFactory,
) (extraInformers []rootapiserver.InformerStart, workspaces []framework.VirtualWorkspace, err error) {

	inf, vws, err := o.Workspaces.NewVirtualWorkspaces(rootPathPrefix, kubeClusterClient, dynamicClusterClient, kcpClusterClient, wildcardKubeInformers, wildcardKcpInformers)
	if err != nil {
		return nil, nil, err
	}
	extraInformers = append(extraInformers, inf...)
	workspaces = append(workspaces, vws...)

	vw := apiexport.NewVirtualWorkspace(rootPathPrefix, dynamicClusterClient, wildcardKcpInformers.Apis().V1alpha1().APIExports(), wildcardKcpInformers.Apis().V1alpha1().APIResourceSchemas())
	workspaces = append(workspaces, vw)

	return extraInformers, workspaces, nil
}
