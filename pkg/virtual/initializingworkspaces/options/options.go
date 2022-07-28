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

	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/spf13/pflag"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	kcpinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
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
	config *rest.Config,
	wildcardKcpInformers kcpinformer.SharedInformerFactory,
) (workspaces map[string]framework.VirtualWorkspace, err error) {
	config = rest.AddUserAgent(rest.CopyConfig(config), "initializingworkspaces-virtual-workspace")
	kubeClusterClient, err := kubernetes.NewForConfig(kcpclienthelper.NewClusterConfig(config))
	if err != nil {
		return nil, err
	}
	dynamicClusterClient, err := dynamic.NewClusterForConfig(config)
	if err != nil {
		return nil, err
	}

	return builder.BuildVirtualWorkspace(config, path.Join(rootPathPrefix, initializingworkspaces.VirtualWorkspaceName), dynamicClusterClient, kubeClusterClient, wildcardKcpInformers)
}
