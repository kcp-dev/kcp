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
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/rootapiserver"

	"github.com/kcp-dev/kcp/pkg/apiidentity"
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
	_ *rest.Config,
	cacheConfig *rest.Config,
	cachedKcpInformers kcpinformers.SharedInformerFactory,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	// We assume the cacheConfig already has the cache-related roundtrippers applied.
	cacheDynamicClusterClient, err := kcpdynamic.NewForConfig(cacheConfig)
	if err != nil {
		return nil, err
	}

	// the identity view follows the APIExports of every shard, replicated
	// to the cache server.
	identities := apiidentity.NewProjector(apiidentity.IsCoreExport)
	_, _ = cachedKcpInformers.Apis().V1alpha2().APIExports().Informer().AddEventHandler(identities)

	return builder.BuildVirtualWorkspace(
		path.Join(rootPathPrefix, admin.VirtualWorkspaceName),
		cacheDynamicClusterClient,
		identities,
	)
}
