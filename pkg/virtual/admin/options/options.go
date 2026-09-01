/*
Copyright 2026 The kcp Authors.

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
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	"github.com/kcp-dev/kcp/pkg/virtual/admin"
	"github.com/kcp-dev/kcp/pkg/virtual/admin/builder"
)

type Admin struct{}

func New() *Admin {
	return &Admin{}
}

func (o *Admin) AddFlags(flags *pflag.FlagSet, prefix string) {
}

func (o *Admin) Validate(flagPrefix string) []error {
	return nil
}

func (o *Admin) NewAdmin(
	rootPathPrefix string,
	config *rest.Config,
	cacheConfig *rest.Config,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	config = rest.AddUserAgent(rest.CopyConfig(config), "admin-virtual-workspace")
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	// We assume the cacheConfig already has the cache-related roundtrippers applied.
	cacheDynamicClusterClient, err := kcpdynamic.NewForConfig(cacheConfig)
	if err != nil {
		return nil, err
	}

	return builder.BuildVirtualWorkspace(
		path.Join(rootPathPrefix, admin.VirtualWorkspaceName),
		cacheDynamicClusterClient,
		kubeClusterClient,
	)
}
