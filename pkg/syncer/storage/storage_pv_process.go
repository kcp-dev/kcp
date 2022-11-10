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

package storage

import (
	"context"
	"fmt"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

func (c *PersistentVolumeController) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "Invalid key", key)
		return nil
	}

	logger = logger.WithValues(logging.WorkspaceKey, clusterName, logging.NameKey, name)

	upstreamPVObject, err := c.getUpstreamPersistentVolume(clusterName, name)
	if err != nil {
		return fmt.Errorf("failed to get upstream PersistentVolume: %w", err)
	}

	if upstreamPVObject == nil {
		logger.V(4).Info("upstream persistent volume not found")
		return nil
	}

	upstreamPVUnstructured, ok := upstreamPVObject.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("failed to assert object to Unstructured: %T", upstreamPVObject)
	}

	logger.V(1).Info("processing upstream PersistentVolume")

	// TODO: Search for the PV downstream with the same name as the one upstream
	// => No need for a Namespace locator.

	upstreamPVUnstructured.GetName()

	downstreamPVObject, err := c.getDownstreamPersistentVolume(ctx, upstreamPVUnstructured.GetName())
	if apierrors.IsNotFound(err) {
		logger.V(4).Info("downstream persistent volume not found, ignoring key")
		return nil
	}
	if err != nil {
		logger.Error(err, "failed to get downstream persistent volume")
		return nil
	}

	if downstreamPVObject == nil {
		logger.Info("downstream persistent volume is nil, ignoring key")
		return nil
	}

	downstreamPVUnstructured, ok := downstreamPVObject.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("failed to assert object to Unstructured: %T", downstreamPVObject)
	}

	if downstreamPVLocator, exists, err := shared.LocatorFromAnnotations(downstreamPVUnstructured.GetAnnotations()); err != nil {
		return err
	} else if !exists {
		logger.Info("downstream persistent volume locator not found, ignoring key")
		return nil
	} else if downstreamPVLocator.SyncTarget.ClusterName != c.syncTarget.workspace.String() ||
		downstreamPVLocator.SyncTarget.Name != c.syncTarget.name ||
		downstreamPVLocator.SyncTarget.UID != c.syncTarget.uid ||
		downstreamPVLocator.ClusterName != clusterName {
		logger.Info("downstream persistent volume locator does not match, ignoring key")
		return nil
	}

	var downstreamPV = &corev1.PersistentVolume{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(downstreamPVUnstructured.UnstructuredContent(), downstreamPV)
	if err != nil {
		return fmt.Errorf("failed to convert unstructured to PersistentVolumeClaim: %w", err)
	}

	downstreamPVCObject, err := c.getDownstreamPersistentVolumeClaim(downstreamPV.Spec.ClaimRef.Name, downstreamPV.Spec.ClaimRef.Namespace)
	if err != nil {
		return fmt.Errorf("failed to get downstream PersistentVolumeClaim: %w", err)
	}

	downstreamPVCObjectUnstructured, ok := downstreamPVCObject.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("failed to assert object to Unstructured: %T", downstreamPVCObject)
	}

	downstreamPVC := &corev1.PersistentVolumeClaim{}
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(downstreamPVCObjectUnstructured.UnstructuredContent(), downstreamPVC)
	if err != nil {
		return fmt.Errorf("failed to convert unstructured to PersistentVolumeClaim: %w", err)
	}

	// Remove the internal.workload.kcp.dev/delaystatussyncing annotation
	annotations := downstreamPVC.GetAnnotations()
	if annotations == nil {
		return nil
	}

	if _, exists := annotations[DelayStatusSyncing]; exists {
		delete(annotations, DelayStatusSyncing)
		downstreamPVC.SetAnnotations(annotations)

		logger.V(1).Info("Removing DelayStatusSyncing annotation", "name", downstreamPVC.Name)
		_, err = c.updateDownstreamPersistentVolumeClaim(ctx, downstreamPVC)
		if err != nil {
			return fmt.Errorf("failed to update downstream PersistentVolumeClaim: %w", err)
		}
	}
	return nil
}
