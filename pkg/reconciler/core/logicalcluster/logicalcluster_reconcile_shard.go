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

package logicalcluster

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

// shardAnnotationReconciler stamps the shard name as an annotation on the LogicalCluster.
// This is separate from the metadataReconciler since this will also
// requires updating the same annotation on the owning object, which may
// live on another shard.
type shardAnnotationReconciler struct {
	shardName  string
	getOwner   func(ctx context.Context, owner corev1alpha1.LogicalClusterOwner) (metav1.Object, error)
	patchOwner func(ctx context.Context, owner corev1alpha1.LogicalClusterOwner, patch []byte) error
}

func (r *shardAnnotationReconciler) reconcile(ctx context.Context, lc *corev1alpha1.LogicalCluster) (reconcileStatus, error) {
	// matches, nothing to do; if the local annotation is correct it is
	// assumed that the annotation on the owner is also correct,
	// otherwise each reconcile would incur additional front-proxy
	// traffic just to check the annotation on the owner.
	if lc.Annotations[corev1alpha1.LogicalClusterShardAnnotationKey] == r.shardName {
		return reconcileStatusContinue, nil
	}

	prev := maps.Clone(lc.Annotations)
	if lc.Annotations == nil {
		lc.Annotations = map[string]string{}
	}
	lc.Annotations[corev1alpha1.LogicalClusterShardAnnotationKey] = r.shardName

	if err := r.setAnnotationOnOwner(ctx, lc); err != nil {
		// revert annotation on LC so annotation doesn't get committed
		// and skips updating owner on next reconcile
		lc.Annotations = prev
		return reconcileStatusStopAndRequeue, err
	}
	return reconcileStatusStopAndRequeue, nil
}

func (r *shardAnnotationReconciler) setAnnotationOnOwner(ctx context.Context, lc *corev1alpha1.LogicalCluster) error {
	if lc.Spec.Owner == nil {
		return nil
	}
	owner := *lc.Spec.Owner

	obj, err := r.getOwner(ctx, owner)
	if err != nil {
		return err
	}
	if obj.GetAnnotations()[corev1alpha1.LogicalClusterShardAnnotationKey] == r.shardName {
		return nil
	}

	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				corev1alpha1.LogicalClusterShardAnnotationKey: r.shardName,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to marshal shard annotation patch: %w", err)
	}

	klog.FromContext(ctx).V(3).Info("patching owner shard annotation",
		"apiVersion", owner.APIVersion,
		"resource", owner.Resource,
		"cluster", owner.Cluster,
		"namespace", owner.Namespace,
		"name", owner.Name,
		"shard", r.shardName,
	)
	return r.patchOwner(ctx, owner, patch)
}
