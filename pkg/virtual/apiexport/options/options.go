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
	"path"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/spf13/pflag"

	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/pkg/authorization"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/builder"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
)

type APIExport struct{}

func New() *APIExport {
	return &APIExport{}
}

func (o *APIExport) AddFlags(flags *pflag.FlagSet, prefix string) {
	if o == nil {
		return
	}
}

func (o *APIExport) Validate(flagPrefix string) []error {
	if o == nil {
		return nil
	}
	errs := []error{}

	return errs
}

func (o *APIExport) NewVirtualWorkspaces(
	rootPathPrefix string,
	config *rest.Config,
	cachedKcpInformers kcpinformers.SharedInformerFactory,
) (workspaces []rootapiserver.NamedVirtualWorkspace, err error) {
	config = rest.AddUserAgent(rest.CopyConfig(config), "apiexport-virtual-workspace")
	kcpClusterClient, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	dynamicClusterClient, err := kcpdynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	deepSARClient, err := kcpkubernetesclientset.NewForConfig(authorization.WithDeepSARConfig(rest.CopyConfig(config)))
	if err != nil {
		return nil, err
	}

	return builder.BuildVirtualWorkspace(path.Join(rootPathPrefix, builder.VirtualWorkspaceName), kubeClusterClient, deepSARClient, dynamicClusterClient, kcpClusterClient, cachedKcpInformers)
}
