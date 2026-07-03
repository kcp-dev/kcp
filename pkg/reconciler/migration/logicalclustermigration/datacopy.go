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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	kcpsdkclient "github.com/kcp-dev/sdk/client/clientset/versioned"

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

	originShard, err := c.shardLister.Cluster(core.RootCluster).Get(originShardName)
	if err != nil {
		return 0, "", fmt.Errorf("failed to get origin shard %q: %w", originShardName, err)
	}
	if originShard.Spec.VirtualWorkspaceURL == "" {
		return 0, "", fmt.Errorf("origin shard %q has no VirtualWorkspaceURL", originShardName)
	}

	cfg := rest.CopyConfig(c.externalLogicalClusterAdminConfig)
	cfg.Host = originShard.Spec.VirtualWorkspaceURL + migratingworkspaces.URLFor() + "/clusters/" + string(lcName)

	client, err := kcpsdkclient.NewForConfig(cfg)
	if err != nil {
		return 0, "", fmt.Errorf("failed to create origin migrating client: %w", err)
	}

	logger.V(2).Info("requesting logical cluster dump page from origin", "logicalCluster", lcName, "originShard", originShardName, "continue", continueToken)

	dump, err := client.MigrationV1alpha1().LogicalClusterDumps().Create(ctx, &migrationv1alpha1.LogicalClusterDump{
		Spec: migrationv1alpha1.LogicalClusterDumpSpec{
			Continue: continueToken,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return 0, "", fmt.Errorf("failed to dump logical cluster from origin: %w", err)
	}

	destPrefix := c.etcdStoragePrefix
	if !strings.HasSuffix(destPrefix, "/") {
		destPrefix += "/"
	}

	logger.V(2).Info("writing dump page entries to local etcd", "logicalCluster", lcName, "entries", len(dump.Status.Entries))

	for _, entry := range dump.Status.Entries {
		if err := ctx.Err(); err != nil {
			return 0, "", err
		}
		key := destPrefix + strings.TrimPrefix(entry.Key, "/")
		if _, err := c.etcdClient.Put(ctx, key, string(entry.Value)); err != nil {
			return 0, "", fmt.Errorf("failed to write etcd key %q: %w", key, err)
		}
	}

	return int64(len(dump.Status.Entries)), dump.Status.Continue, nil
}
