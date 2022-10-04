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

	"github.com/kcp-dev/logicalcluster/v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

const (
	// SyncerFinalizerNamePrefix is the finalizer put onto resources by the syncer to claim ownership,
	// *before* a downstream object is created. It is only removed when the downstream object is deleted.
	SyncerFinalizerNamePrefix = "workload.kcp.dev/syncer-"
)

func EnsureUpstreamFinalizerRemoved(ctx context.Context, gvr schema.GroupVersionResource, upstreamInformer informers.GenericInformer, upstreamClient dynamic.ClusterInterface, upstreamNamespace, syncTargetKey string, logicalClusterName logicalcluster.Name, resourceName string) error {
	upstreamObjFromLister, err := upstreamInformer.Lister().ByNamespace(upstreamNamespace).Get(clusters.ToClusterAwareKey(logicalClusterName, resourceName))
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if apierrors.IsNotFound(err) {
		return nil
	}

	upstreamObj, ok := upstreamObjFromLister.(*unstructured.Unstructured)
	if !ok {
		klog.Errorf("Resource %s|%s/%s expected to be *unstructured.Unstructured, got %T", logicalClusterName.String(), upstreamNamespace, resourceName, upstreamObjFromLister)
		return nil
	}

	// TODO(jmprusi): This check will need to be against "GetDeletionTimestamp()" when using the syncer virtual workspace.
	if upstreamObj.GetAnnotations()[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+syncTargetKey] == "" {
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

	//  TODO(jmprusi): This code block will be handled by the syncer virtual workspace, so we can remove it once
	//                 the virtual workspace syncer is integrated
	//  - Begin -
	// Clean up the status annotation and the locationDeletionAnnotation.
	annotations := upstreamObj.GetAnnotations()
	delete(annotations, workloadv1alpha1.InternalClusterStatusAnnotationPrefix+syncTargetKey)
	delete(annotations, workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+syncTargetKey)
	upstreamObj.SetAnnotations(annotations)

	// remove the cluster label.
	upstreamLabels := upstreamObj.GetLabels()
	delete(upstreamLabels, workloadv1alpha1.ClusterResourceStateLabelPrefix+syncTargetKey)
	upstreamObj.SetLabels(upstreamLabels)
	// - End of block to be removed once the virtual workspace syncer is integrated -

	if _, err := upstreamClient.Cluster(logicalClusterName).Resource(gvr).Namespace(upstreamObj.GetNamespace()).Update(ctx, upstreamObj, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("Failed updating after removing the finalizers of resource %s|%s/%s: %v", logicalClusterName, upstreamNamespace, upstreamObj.GetName(), err)
		return err
	}
	klog.V(2).Infof("Updated resource %s|%s/%s after removing the finalizers", logicalClusterName, upstreamNamespace, upstreamObj.GetName())
	return nil
}
