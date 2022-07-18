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

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
)

// Bootstrap creates resources in this package by continuously retrying the list.
// This is blocking, i.e. it only returns (with error) when the context is closed or with nil when
// the bootstrapping is successfully completed.
func Bootstrap(ctx context.Context, kcpClient kcpclientset.Interface) error {
	// note: shards are not really needed. But to avoid breaking the kcp shared informer factory, we also add them.
	return confighelpers.BindRootAPIs(ctx, kcpClient, "shards.tenancy.kcp.dev", "tenancy.kcp.dev", "scheduling.kcp.dev", "workload.kcp.dev", "apiresource.kcp.dev")
}
