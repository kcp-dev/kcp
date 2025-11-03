/*
Copyright 2025 The KCP Authors.

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

	"k8s.io/client-go/rest"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/terminatingworkspaces"
	"github.com/kcp-dev/kcp/pkg/virtual/terminatingworkspaces/builder"
)

type TerminatingWorkspaces struct{}

func New() *TerminatingWorkspaces {
	return &TerminatingWorkspaces{}
}

func (o *TerminatingWorkspaces) AddFlags(flags *pflag.FlagSet, prefix string) {
	if o == nil {
		return
	}
}

func (o *TerminatingWorkspaces) Validate(flagPrefix string) []error {
	if o == nil {
		return nil
	}
	errs := []error{}

	return errs
}

func (o *TerminatingWorkspaces) NewVirtualWorkspaces(
	rootPathPrefix string,
	config *rest.Config,
	wildcardKcpInformers kcpinformers.SharedInformerFactory,
) (workspaces []rootapiserver.NamedVirtualWorkspace, err error) {
	config = rest.AddUserAgent(rest.CopyConfig(config), "terminatingworkspaces-virtual-workspace")
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	dynamicClusterClient, err := kcpdynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return builder.BuildVirtualWorkspace(config, path.Join(rootPathPrefix, terminatingworkspaces.VirtualWorkspaceName), dynamicClusterClient, kubeClusterClient)
}
