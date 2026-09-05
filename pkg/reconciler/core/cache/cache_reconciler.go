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
	"maps"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	configshard "github.com/kcp-dev/kcp/config/shard"
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
	if globalCacheObj != nil && !globalCacheObj.DeletionTimestamp.IsZero() {
		shouldDeleteLocally = true
	}
	if shouldDeleteLocally {
		logger.Info("Global Cache object deleted, deleting local copy")
		err := c.deleteLocalCacheObj(ctx, configshard.SystemShardCluster, name)
		if !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	localCacheObj, err := c.getLocalCacheObj(configshard.SystemShardCluster, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("Creating local copy of Cache object")
			globalCacheObj = globalCacheObj.DeepCopy()
			globalCacheObj.ResourceVersion = ""
			_, err = c.createLocalCacheObj(ctx, globalCacheObj)
			return err
		}
		return err
	}

	if !isUpToDate(globalCacheObj, localCacheObj) {
		logger.Info("Updating local copy of Cache object")
		globalCacheObj = globalCacheObj.DeepCopy()
		globalCacheObj.UID = localCacheObj.UID
		globalCacheObj.ResourceVersion = localCacheObj.ResourceVersion
		_, err = c.updateLocalCacheObj(ctx, globalCacheObj)
		return err
	}

	return nil
}

func isUpToDate(a, b *corev1alpha1.Cache) bool {
	type visitFn func(obj *corev1alpha1.Cache) any
	compareField := func(visit visitFn) bool {
		return reflect.DeepEqual(visit(a), visit(b))
	}

	for _, visit := range []visitFn{
		func(obj *corev1alpha1.Cache) any {
			ann := make(map[string]string)
			maps.Copy(ann, obj.Annotations)
			delete(ann, logicalcluster.AnnotationKey)
			return ann
		},
		func(obj *corev1alpha1.Cache) any { return obj.Labels },
		func(obj *corev1alpha1.Cache) any { return obj.Spec },
		func(obj *corev1alpha1.Cache) any { return obj.Status },
	} {
		if !compareField(visit) {
			return false
		}
	}

	return true
}
