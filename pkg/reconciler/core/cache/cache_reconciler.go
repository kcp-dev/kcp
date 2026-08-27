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

package cache

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
)

func (c *Controller) reconcile(ctx context.Context, clusterName logicalcluster.Name, name string) error {
	// Replicate the Cache obj from cache-server to shard.

	logger := klog.FromContext(ctx)

	shouldDeleteLocally := false

	globalCacheObj, err := c.getGlobalCacheObj(clusterName, name)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		shouldDeleteLocally = true
	}
	if !globalCacheObj.DeletionTimestamp.IsZero() {
		shouldDeleteLocally = true
	}
	if shouldDeleteLocally {
		logger.Info("Global Cache object deleted, deleting local copy")
		err := c.deleteLocalCacheObj(ctx, clusterName, name)
		if !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	localCacheObj, err := c.getLocalCacheObj(clusterName, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Creating local copy of Cache object")
			_, err = c.createLocalCacheObj(ctx, globalCacheObj)
			return err
		}
		return err
	}

	logger.Info("Updating local copy of Cache object")

	globalCacheObj = globalCacheObj.DeepCopy()
	globalCacheObj.UID = localCacheObj.UID
	globalCacheObj.ResourceVersion = localCacheObj.ResourceVersion
	_, err = c.updateLocalCacheObj(ctx, globalCacheObj)
	return err
}
