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

package apiidentity

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
)

// adminv1alpha1client is the slice of the APIIdentity client the controller
// uses, so tests can fake it.
type adminv1alpha1client interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*adminv1alpha1.APIIdentity, error)
	Update(ctx context.Context, entry *adminv1alpha1.APIIdentity, opts metav1.UpdateOptions) (*adminv1alpha1.APIIdentity, error)
}

// registryScoped addresses the registry's shard scope on the cache server.
type registryScoped struct {
	adminv1alpha1client
}

func (r registryScoped) Get(ctx context.Context, name string, opts metav1.GetOptions) (*adminv1alpha1.APIIdentity, error) {
	return r.adminv1alpha1client.Get(cacheclient.WithShardInContext(ctx, shard.Registry), name, opts)
}

func (r registryScoped) Update(ctx context.Context, entry *adminv1alpha1.APIIdentity, opts metav1.UpdateOptions) (*adminv1alpha1.APIIdentity, error) {
	return r.adminv1alpha1client.Update(cacheclient.WithShardInContext(ctx, shard.Registry), entry, opts)
}
