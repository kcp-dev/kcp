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

	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	kcpetcd "github.com/kcp-dev/kcp/pkg/etcd"
)

// deleteOriginData removes all etcd data belonging to the given logical cluster.
func (c *Controller) deleteOriginData(ctx context.Context, lcName logicalcluster.Name) error {
	logger := klog.FromContext(ctx)
	logger.V(2).Info("cleaning up origin data via etcd", "logicalCluster", lcName)

	prefix := c.etcdStoragePrefix
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	// TODO(ntnn): This isn't very solid and just assumes the happy
	// path. The deletion should probably stop and requeue on any error.

	prefixes := make(chan string)
	errCh := make(chan error, 1)

	go func() {
		errCh <- scanEtcdKeys(ctx, c.etcdClient, prefix, lcName, kcpetcd.ScanPageSize, prefixes)
	}()

	var deleteErrors []error
	for p := range prefixes {
		logger.V(4).Info("deleting etcd prefix", "prefix", p)
		if _, err := c.etcdClient.Delete(ctx, p, clientv3.WithPrefix()); err != nil {
			deleteErrors = append(deleteErrors, fmt.Errorf("failed to delete prefix %s: %w", p, err))
		}
	}

	if err := <-errCh; err != nil {
		deleteErrors = append(deleteErrors, err)
	}

	if len(deleteErrors) > 0 {
		return fmt.Errorf("origin cleanup encountered %d errors: %v", len(deleteErrors), deleteErrors)
	}

	return nil
}

// scanEtcdKeys scans etcd under prefix and emits one cluster-scoped prefix
// per (group, resource[, segment]) family that contains keys belonging to
// targetLogicalCluster.
func scanEtcdKeys(ctx context.Context, kv clientv3.KV, prefix string, targetLogicalCluster logicalcluster.Name, pageSize int64, out chan<- string) error {
	defer close(out)

	seen := make(map[string]struct{})

	key := prefix
	for {
		resp, err := kv.Get(ctx, key,
			clientv3.WithRange(clientv3.GetPrefixRangeEnd(prefix)),
			clientv3.WithKeysOnly(),
			clientv3.WithLimit(pageSize),
		)
		if err != nil {
			return fmt.Errorf("failed to list etcd keys: %w", err)
		}

		for _, kv := range resp.Kvs {
			if err := ctx.Err(); err != nil {
				return err
			}

			split, ok := kcpetcd.SplitKey(prefix, string(kv.Key), targetLogicalCluster)
			if !ok {
				continue
			}
			if split.Cluster != targetLogicalCluster {
				continue
			}

			builtPrefix := split.ClusterPrefix(prefix)
			if _, dup := seen[builtPrefix]; dup {
				continue
			}
			seen[builtPrefix] = struct{}{}
			out <- builtPrefix
		}

		if !resp.More {
			return nil
		}
		key = string(resp.Kvs[len(resp.Kvs)-1].Key) + "\x00"
	}
}
