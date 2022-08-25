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

package shard

import (
	"context"
	"embed"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
)

//go:embed *.yaml
var fs embed.FS

// SystemShardCluster is the name of a logical cluster on every shard (including the root shard) that holds essential system resources (like the root APIs).
var SystemShardCluster = logicalcluster.New("system:shard")

// Bootstrap creates resources required for a shard.
// As of today creating API bindings for the root APIs and the default ns is enough.
func Bootstrap(ctx context.Context, discoveryClient discovery.DiscoveryInterface, dynamicClient dynamic.Interface, batteriesIncluded sets.String, kcpClient kcpclientset.Interface, clusterName logicalcluster.Name) error {
	// note: shards are not really needed. But to avoid breaking the kcp shared informer factory, we also add them.
	if err := confighelpers.BindRootAPIs(logicalcluster.WithCluster(ctx, clusterName), kcpClient, "shards.tenancy.kcp.dev", "tenancy.kcp.dev", "scheduling.kcp.dev", "workload.kcp.dev", "apiresource.kcp.dev"); err != nil {
		return err
	}
	return confighelpers.Bootstrap(ctx, discoveryClient, dynamicClient, batteriesIncluded, fs)
}
