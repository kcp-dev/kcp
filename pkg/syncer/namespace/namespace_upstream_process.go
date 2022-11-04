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

package namespace

import (
	"context"
	"fmt"

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	. "github.com/kcp-dev/kcp/tmc/pkg/logging"
)

func (c *UpstreamController) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	clusterName, _, namespaceName, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "invalid key")
		return nil
	}

	logger = logger.WithValues(logging.WorkspaceKey, clusterName, logging.NameKey, namespaceName)

	exists, err := c.upstreamNamespaceExists(clusterName, namespaceName)
	if err != nil {
		logger.Error(err, "failed to check if upstream namespace exists")
		return nil
	}

	if exists {
		logger.Info("upstream namespace exists, nothing to do")
		return nil
	}

	namespaceLocator := shared.NamespaceLocator{
		SyncTarget: shared.SyncTargetLocator{
			Name:      c.syncTargetName,
			UID:       c.syncTargetUID,
			Workspace: c.syncTargetWorkspace.String(),
		},
		Workspace: clusterName,
		Namespace: namespaceName,
	}

	downstreamNamespace, err := c.getDownstreamNamespaceFromNamespaceLocator(namespaceLocator)
	if apierrors.IsNotFound(err) {
		logger.V(4).Info("downstream namespace not found, ignoring key")
		return nil
	} else if err != nil {
		logger.Error(err, "failed to get downstream namespace")
		return nil
	}

	if downstreamNamespace == nil {
		logger.Info("downstream namespace not found, ignoring key")
		return nil
	}

	downstreamNamespaceName := downstreamNamespace.(*unstructured.Unstructured).GetName()
	logger = logger.WithValues(DownstreamNamespace, downstreamNamespaceName)
	// Check if the namespace is empty before deleting trying to delete it
	if empty, err := c.isDowntreamNamespaceEmpty(ctx, downstreamNamespaceName); err != nil {
		return fmt.Errorf("failed to check if downstream namespace %q is empty: %w", downstreamNamespaceName, err)
	} else if !empty {
		return fmt.Errorf("downstream namespace %q is not empty", downstreamNamespaceName)
	}
	logger.Info("adding the downstream namespace to the delayed deletion queue because the upstream namespace doesn't exist and is empty.")
	c.namespaceCleaner.PlanCleaning(downstreamNamespaceName)
	return nil
}
