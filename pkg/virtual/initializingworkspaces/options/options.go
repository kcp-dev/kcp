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

	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces"
	"github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces/builder"
)

type InitializingWorkspaces struct{}

func New() *InitializingWorkspaces {
	return &InitializingWorkspaces{}
}

func (o *InitializingWorkspaces) AddFlags(flags *pflag.FlagSet, prefix string) {
	if o == nil {
		return
	}
}

func (o *InitializingWorkspaces) Validate(flagPrefix string) []error {
	if o == nil {
		return nil
	}
	errs := []error{}

	return errs
}

func (o *InitializingWorkspaces) NewVirtualWorkspaces(
	rootPathPrefix string,
	dynamicClusterClient dynamic.ClusterInterface,
	kubeClusterClient kubernetes.ClusterInterface,
	wildcardApiExtensionsInformers apiextensionsinformers.SharedInformerFactory,
) (extraInformers []rootapiserver.InformerStart, workspaces []framework.VirtualWorkspace, err error) {
	virtualWorkspaces := []framework.VirtualWorkspace{
		builder.BuildVirtualWorkspace(path.Join(rootPathPrefix, o.Name()), dynamicClusterClient, kubeClusterClient, wildcardApiExtensionsInformers),
	}
	return nil, virtualWorkspaces, nil
}

func (o *InitializingWorkspaces) Name() string {
	return initializingworkspaces.VirtualWorkspaceName
}
