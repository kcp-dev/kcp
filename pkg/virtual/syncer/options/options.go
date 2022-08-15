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

	"github.com/spf13/pflag"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/builder"
)

type Syncer struct{}

func New() *Syncer {
	return &Syncer{}
}

func (o *Syncer) AddFlags(flags *pflag.FlagSet, prefix string) {
	if o == nil {
		return
	}
}

func (o *Syncer) Validate(flagPrefix string) []error {
	if o == nil {
		return nil
	}
	errs := []error{}

	return errs
}

func (o *Syncer) NewVirtualWorkspaces(
	rootPathPrefix string,
	config *rest.Config,
	wildcardKcpInformers kcpinformer.SharedInformerFactory,
) (workspaces []rootapiserver.NamedVirtualWorkspace, err error) {
	config = rest.AddUserAgent(rest.CopyConfig(config), "syncer-virtual-workspace")
	kcpClusterClient, err := kcpclient.NewClusterForConfig(config)
	if err != nil {
		return nil, err
	}
	kubeClusterClient, err := kubernetes.NewClusterForConfig(config)
	if err != nil {
		return nil, err
	}
	dynamicClusterClient, err := dynamic.NewClusterForConfig(config)
	if err != nil {
		return nil, err
	}

	return []rootapiserver.NamedVirtualWorkspace{
		{Name: builder.SyncerVirtualWorkspaceName, VirtualWorkspace: builder.BuildVirtualWorkspace(path.Join(rootPathPrefix, builder.SyncerVirtualWorkspaceName), kubeClusterClient, dynamicClusterClient, kcpClusterClient, wildcardKcpInformers)},
	}, nil
}
