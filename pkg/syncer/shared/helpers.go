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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kcp-dev/logicalcluster"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

const (
	// SyncerFinalizerNamePrefix is the finalizer put onto resources by the syncer to claim ownership,
	// *before* a downstream object is created. It is only removed when the downstream object is deleted.
	SyncerFinalizerNamePrefix = "workloads.kcp.dev/syncer-"

	NamespaceLocatorAnnotation = "kcp.dev/namespace-locator"
)

// DeprecatedGetAssignedWorkloadCluster returns one assigned workload cluster in Sync state. It will
// likely lead to broken behaviour when there is one of those labels on a resource.
//
// Deprecated: use GetResourceState per cluster instead.
func DeprecatedGetAssignedWorkloadCluster(labels map[string]string) string {
	for k, v := range labels {
		if strings.HasPrefix(k, workloadv1alpha1.InternalClusterResourceStateLabelPrefix) && v == string(workloadv1alpha1.ResourceStateSync) {
			return strings.TrimPrefix(k, workloadv1alpha1.InternalClusterResourceStateLabelPrefix)
		}
	}
	return ""
}

// NamespaceLocator stores a logical cluster and namespace and is used
// as the source for the mapped namespace name in a physical cluster.
type NamespaceLocator struct {
	LogicalCluster logicalcluster.Name `json:"logical-cluster"`
	Namespace      string              `json:"namespace"`
}

func LocatorFromAnnotations(annotations map[string]string) (*NamespaceLocator, error) {
	annotation := annotations[NamespaceLocatorAnnotation]
	if len(annotation) == 0 {
		return nil, nil
	}
	var locator NamespaceLocator
	if err := json.Unmarshal([]byte(annotation), &locator); err != nil {
		return nil, err
	}
	return &locator, nil
}

// PhysicalClusterNamespaceName encodes the NamespaceLocator to a new
// namespace name for use on a physical cluster. The encoding is repeatable.
func PhysicalClusterNamespaceName(l NamespaceLocator) (string, error) {
	b, err := json.Marshal(l)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum224(b)
	return fmt.Sprintf("kcp%x", hash), nil
}

func EnsureUpstreamFinalizerRemoved(ctx context.Context, gvr schema.GroupVersionResource, upstreamClient dynamic.Interface, upstreamNamespace, workloadClusterName string, logicalClusterName logicalcluster.Name, resourceName string) error {
	upstreamObj, err := upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Get(ctx, resourceName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if apierrors.IsNotFound(err) {
		return nil
	}

	// TODO(jmprusi): This check will need to be against "GetDeletionTimestamp()" when using the syncer virtual  workspace.
	if upstreamObj.GetAnnotations()[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+workloadClusterName] == "" {
		// Do nothing: the object should not be deleted anymore for this location on the KCP side
		return nil
	}

	// Remove the syncer finalizer.
	currentFinalizers := upstreamObj.GetFinalizers()
	desiredFinalizers := []string{}
	for _, finalizer := range currentFinalizers {
		if finalizer != SyncerFinalizerNamePrefix+workloadClusterName {
			desiredFinalizers = append(desiredFinalizers, finalizer)
		}
	}
	upstreamObj.SetFinalizers(desiredFinalizers)

	//  TODO(jmprusi): This code block will be handled by the syncer virtual workspace, so we can remove it once
	//                 the virtual workspace syncer is integrated
	//  - Begin -
	// Clean up the status annotation and the locationDeletionAnnotation.
	annotations := upstreamObj.GetAnnotations()
	delete(annotations, workloadv1alpha1.InternalClusterStatusAnnotationPrefix+workloadClusterName)
	delete(annotations, workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+workloadClusterName)
	delete(annotations, workloadv1alpha1.InternalClusterStatusAnnotationPrefix+workloadClusterName)
	upstreamObj.SetAnnotations(annotations)

	// remove the cluster label.
	upstreamLabels := upstreamObj.GetLabels()
	delete(upstreamLabels, workloadv1alpha1.InternalClusterResourceStateLabelPrefix+workloadClusterName)
	upstreamObj.SetLabels(upstreamLabels)
	// - End of block to be removed once the virtual workspace syncer is integrated -

	if _, err := upstreamClient.Resource(gvr).Namespace(upstreamObj.GetNamespace()).Update(ctx, upstreamObj, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("Failed updating after removing the finalizers of resource %s|%s/%s: %v", logicalClusterName, upstreamNamespace, upstreamObj.GetName(), err)
		return err
	}
	klog.V(2).Infof("Updated resource %s|%s/%s after removing the finalizers", logicalClusterName, upstreamNamespace, upstreamObj.GetName())
	return nil
}
