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

package logicalclustermigration

import (
	"context"
	"fmt"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/storage/value"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	kcpetcd "github.com/kcp-dev/kcp/pkg/etcd"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/virtual/migratingworkspaces"
)

// copyPageFromOriginViaHTTP requests a single page of a LogicalClusterDump
// from the origin shard's migrating virtual workspace, starting after
// continueToken, and writes the returned etcd entries directly to the local
// shard's etcd.
//
// It returns the number of entries written and the continue token for the
// next page. An empty next continue token means the copy is complete.
//
// This is assigned to Controller.copyPageFromOrigin at construction time;
// reconcileMigrating calls that field rather than this method directly so
// tests can substitute a fake.
func (c *Controller) copyPageFromOriginViaHTTP(ctx context.Context, lcName logicalcluster.Name, originShardName, continueToken string) (int64, string, error) {
	logger := klog.FromContext(ctx)

	shardClient, err := c.acquireOriginClient(lcName, originShardName)
	if err != nil {
		return 0, "", err
	}
	client := shardClient.Cluster(lcName.Path())

	logger.V(2).Info("requesting logical cluster dump page from origin", "logicalCluster", lcName, "originShard", originShardName, "continue", continueToken)

	dump, err := client.MigrationV1alpha1().LogicalClusterDumps().Create(ctx, &migrationv1alpha1.LogicalClusterDump{
		Spec: migrationv1alpha1.LogicalClusterDumpSpec{
			Encoding: migrationv1alpha1.DumpEncodingPlaintext,
			Continue: continueToken,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return 0, "", fmt.Errorf("failed to dump logical cluster from origin: %w", err)
	}

	if dump.Status.Encoding != migrationv1alpha1.DumpEncodingPlaintext {
		return 0, "", fmt.Errorf("origin returned unsupported dump encoding %q", dump.Status.Encoding)
	}
	if err := writeDumpEntries(ctx, c.etcdClient, c.etcdStoragePrefix, c.resourceTransformers, dump.Status.Entries); err != nil {
		return 0, "", err
	}

	return int64(len(dump.Status.Entries)), dump.Status.Continue, nil
}

// writeDumpEntries applies the destination's current resource transformers using
// the complete destination key as authenticated data before writing each entry.
func writeDumpEntries(ctx context.Context, kv clientv3.KV, prefix string, transformers value.ResourceTransformers, entries []migrationv1alpha1.EtcdEntry) error {
	prefix = strings.TrimRight(prefix, "/") + "/"
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		relativeKey := strings.TrimPrefix(entry.Key, "/")
		transformer, err := kcpetcd.TransformerForKey(transformers, relativeKey)
		if err != nil {
			return err
		}
		key := prefix + relativeKey
		data, err := transformer.TransformToStorage(ctx, entry.Value, value.DefaultContext(key))
		if err != nil {
			return fmt.Errorf("failed to transform destination key %q: %w", key, err)
		}
		if _, err := kv.Put(ctx, key, string(data)); err != nil {
			return fmt.Errorf("failed to write etcd key %q: %w", key, err)
		}
	}
	return nil
}

// acquireOriginClient returns the shared, cluster-aware client for
// originShardName, creating it if none exists yet, and records lcName as a
// user of it. The underlying client is scoped to the origin shard's
// migrating virtual workspace as a whole (not to any one logical cluster),
// so every concurrent migration copying data from the same origin shard
// reuses the same HTTP transport and connection pool - callers scope it to
// their own logical cluster per request via shardClient.Cluster(path).
//
// Safe to call repeatedly for the same (originShardName, lcName) pair, e.g.
// once per page of the same copy: recording lcName as a user is idempotent,
// so it doesn't inflate a refcount.
func (c *Controller) acquireOriginClient(lcName logicalcluster.Name, originShardName string) (kcpclientset.ClusterInterface, error) {
	c.originClientsMu.Lock()
	defer c.originClientsMu.Unlock()

	entry, ok := c.originClients[originShardName]
	if !ok {
		originShard, err := indexers.ShardByName(c.shardLister, originShardName)
		if err != nil {
			return nil, fmt.Errorf("failed to get origin shard %q: %w", originShardName, err)
		}
		if originShard.Spec.VirtualWorkspaceURL == "" {
			return nil, fmt.Errorf("origin shard %q has no VirtualWorkspaceURL", originShardName)
		}

		cfg := rest.CopyConfig(c.externalLogicalClusterAdminConfig)
		cfg.Host = originShard.Spec.VirtualWorkspaceURL + migratingworkspaces.URLFor()

		client, err := kcpclientset.NewForConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create origin migrating client: %w", err)
		}

		entry = &originClientEntry{client: client, refs: map[logicalcluster.Name]struct{}{}}
		c.originClients[originShardName] = entry
	}

	entry.refs[lcName] = struct{}{}
	return entry.client, nil
}

// releaseOriginClient marks lcName as done with originShardName's client.
// Once no logical cluster is using it anymore, the shared client is
// dropped from the cache instead of lingering indefinitely. Safe to call
// even if lcName never acquired the client (e.g. the migration failed
// before ever reaching copyPageFromOriginViaHTTP).
func (c *Controller) releaseOriginClient(lcName logicalcluster.Name, originShardName string) {
	c.originClientsMu.Lock()
	defer c.originClientsMu.Unlock()

	entry, ok := c.originClients[originShardName]
	if !ok {
		return
	}
	delete(entry.refs, lcName)
	if len(entry.refs) == 0 {
		delete(c.originClients, originShardName)
	}
}
