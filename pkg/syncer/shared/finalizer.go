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

package shared

import (
	"context"
	"fmt"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	"github.com/kcp-dev/logicalcluster/v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

const (
	// SyncerFinalizerNamePrefix is the finalizer put onto resources by the syncer to claim ownership,
	// *before* a downstream object is created. It is only removed when the downstream object is deleted.
	SyncerFinalizerNamePrefix = "workload.kcp.dev/syncer-"
)

func EnsureUpstreamFinalizerRemoved(ctx context.Context, gvr schema.GroupVersionResource, upstreamInformer kcpkubernetesinformers.GenericClusterInformer, upstreamClient kcpdynamic.ClusterInterface, upstreamNamespace, syncTargetKey string, logicalClusterName logicalcluster.Name, resourceName string) error {
	logger := klog.FromContext(ctx)
	upstreamObjFromLister, err := upstreamInformer.Lister().ByCluster(logicalClusterName).ByNamespace(upstreamNamespace).Get(resourceName)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if apierrors.IsNotFound(err) {
		return nil
	}

	upstreamObj, ok := upstreamObjFromLister.(*unstructured.Unstructured)
	if !ok {
		logger.Info(fmt.Sprintf("Error: upstream resource expected to be *unstructured.Unstructured, got %T", upstreamObjFromLister))
		return nil
	}

	if upstreamObj.GetDeletionTimestamp() == nil {
		// Do nothing: the object should not be deleted anymore for this location on the KCP side
		return nil
	}

	upstreamObj = upstreamObj.DeepCopy()

	// Remove the syncer finalizer.
	currentFinalizers := upstreamObj.GetFinalizers()
	desiredFinalizers := []string{}
	for _, finalizer := range currentFinalizers {
		if finalizer != SyncerFinalizerNamePrefix+syncTargetKey {
			desiredFinalizers = append(desiredFinalizers, finalizer)
		}
	}
	upstreamObj.SetFinalizers(desiredFinalizers)

	// TODO(davidfestal): The following code block should be removed as soon as the Advanced Scheduling feature is
	// removed as well as the corresponding status annotation (superseded by Syncer transformations)
	//  - Begin -
	annotations := upstreamObj.GetAnnotations()
	delete(annotations, workloadv1alpha1.InternalClusterStatusAnnotationPrefix+syncTargetKey)
	upstreamObj.SetAnnotations(annotations)
	// - End -

	if upstreamNamespace != "" {
		_, err = upstreamClient.Cluster(logicalClusterName).Resource(gvr).Namespace(upstreamObj.GetNamespace()).Update(ctx, upstreamObj, metav1.UpdateOptions{})
	} else {
		_, err = upstreamClient.Cluster(logicalClusterName).Resource(gvr).Update(ctx, upstreamObj, metav1.UpdateOptions{})
	}
	if err != nil && !apierrors.IsNotFound(err) {
		logger.Error(err, "Failed updating upstream resource after removing the syncer finalizer")
		return err
	} else if err == nil {
		logger.V(2).Info("Updated upstream resource to remove the syncer finalizer")
	} else {
		logger.V(3).Info("Didn't update upstream resource to remove the syncer finalizer, since the upstream resource doesn't exist anymore")
	}
	return nil
}
