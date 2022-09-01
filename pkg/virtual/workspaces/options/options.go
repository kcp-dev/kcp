/*
Copyright 2021 The KCP Authors.

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
	"path"

	"github.com/spf13/pflag"

	kubernetesinformers "k8s.io/client-go/informers"
	kubernetesclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/workspaces/builder"
)

type Workspaces struct{}

func New() *Workspaces {
	return &Workspaces{}
}

func (o *Workspaces) AddFlags(flags *pflag.FlagSet, prefix string) {
	if o == nil {
		return
	}
}

func (o *Workspaces) Validate(flagPrefix string) []error {
	if o == nil {
		return nil
	}
	errs := []error{}

	return errs
}

func (o *Workspaces) NewVirtualWorkspaces(
	rootPathPrefix string,
	config *rest.Config,
	wildcardKubeInformers kubernetesinformers.SharedInformerFactory,
	wildcardKcpInformers kcpinformers.SharedInformerFactory,
) (workspaces []rootapiserver.NamedVirtualWorkspace, err error) {
	config = rest.AddUserAgent(rest.CopyConfig(config), "workspaces-virtual-workspace")
	kcpClusterClient, err := kcpclient.NewClusterForConfig(config)
	if err != nil {
		return nil, err
	}
	kubeClusterClient, err := kubernetesclient.NewClusterForConfig(config)
	if err != nil {
		return nil, err
	}

	return []rootapiserver.NamedVirtualWorkspace{
		{Name: "workspaces", VirtualWorkspace: builder.BuildVirtualWorkspace(config, path.Join(rootPathPrefix, "workspaces"), wildcardKcpInformers.Tenancy().V1alpha1().ClusterWorkspaces(), wildcardKubeInformers.Rbac().V1(), kubeClusterClient, kcpClusterClient)},
	}, nil
}
