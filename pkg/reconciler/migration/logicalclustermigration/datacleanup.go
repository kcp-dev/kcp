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
)

const etcdPageSize int64 = 1000

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
		defer close(errCh)
		errCh <- c.scanEtcdKeys(ctx, prefix, lcName, prefixes)
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

func (c *Controller) scanEtcdKeys(ctx context.Context, prefix string, targetLogicalCluster logicalcluster.Name, out chan<- string) error {
	defer close(out)

	seen := make(map[string]struct{})

	key := prefix
	for {
		resp, err := c.etcdClient.Get(ctx, key,
			clientv3.WithRange(clientv3.GetPrefixRangeEnd(prefix)),
			clientv3.WithKeysOnly(),
			clientv3.WithLimit(etcdPageSize),
		)
		if err != nil {
			return fmt.Errorf("failed to list etcd keys: %w", err)
		}

		for _, kv := range resp.Kvs {
			if err := ctx.Err(); err != nil {
				return err
			}

			etcdKeyPrefix, logicalClusterName := parseKey(prefix, string(kv.Key))
			if logicalClusterName != targetLogicalCluster+"/" {
				continue
			}
			if _, exists := seen[etcdKeyPrefix]; exists {
				continue
			}
			seen[etcdKeyPrefix] = struct{}{}

			out <- etcdKeyPrefix
		}

		if !resp.More {
			return nil
		}
		key = string(resp.Kvs[len(resp.Kvs)-1].Key) + "\x00"
	}
}

// parseKey parses an etcd key and returns the deletion prefix that scopes
// to a single logical cluster, plus the logical cluster name itself.
func parseKey(prefix, key string) (string, logicalcluster.Name) {
	// /prefix/group/resource/[customresources/]lc/something
	// -> [/]group/resource/[customresources/]lc/something
	key = strings.TrimPrefix(key, prefix)
	// -> group/resource/[customresources/]lc/something
	key = strings.TrimPrefix(key, "/")
	// -> group, resource, [customresources,] lc, something...
	parts := strings.SplitN(key, "/", 5)
	if len(parts) < 3 {
		// Too short to be a resource key
		return "", ""
	}
	//	<prefix>/<group>/<resource>/customresources/<lc>/...
	if parts[2] == "customresources" {
		if len(parts) < 4 {
			return "", ""
		}
		return prefix + parts[0] + "/" + parts[1] + "/customresources/" + parts[3], logicalcluster.Name(parts[3]) + "/"
	}
	//	<prefix>/<group>/<resource>/<lc>/...
	return prefix + parts[0] + "/" + parts[1] + "/" + parts[2], logicalcluster.Name(parts[2]) + "/"
}
