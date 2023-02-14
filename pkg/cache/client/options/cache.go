/*
Copyright 2023 The KCP Authors.

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
	"fmt"

	"github.com/spf13/pflag"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
)

type Cache struct {
	KubeconfigFile string
}

func NewCache() *Cache {
	return &Cache{}
}

func (o *Cache) AddFlags(flags *pflag.FlagSet) {
	flags.StringVar(&o.KubeconfigFile, "cache-server-kubeconfig-file", o.KubeconfigFile, "The kubeconfig file of the cache server instance that hosts workspaces.")
	flags.MarkDeprecated("cache-server-kubeconfig-file", "use --cache-kubeconfig instead") //nolint:errcheck

	flags.StringVar(&o.KubeconfigFile, "cache-kubeconfig", o.KubeconfigFile,
		"The kubeconfig file of the cache server instance that hosts workspaces.")
}

func (o *Cache) Validate() []error {
	return nil
}

func (o *Cache) RestConfig(fallback *rest.Config) (*rest.Config, error) {
	cacheClientConfig := fallback
	if len(o.KubeconfigFile) > 0 {
		var err error
		cacheClientConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: o.KubeconfigFile}, nil).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to load the cache kubeconfig from %q: %w", o.KubeconfigFile, err)
		}
	}

	rt := cacheclient.WithCacheServiceRoundTripper(cacheClientConfig)
	rt = cacheclient.WithShardNameFromContextRoundTripper(rt)
	rt = cacheclient.WithDefaultShardRoundTripper(rt, shard.Wildcard)

	return rt, nil
}
