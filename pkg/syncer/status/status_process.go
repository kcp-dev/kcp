/*
Copyright 2021 The KCP Authors.

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

package status

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	workloadcliplugin "github.com/kcp-dev/kcp/pkg/cliplugins/workload/plugin"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

func deepEqualFinalizersAndStatus(oldUnstrob, newUnstrob *unstructured.Unstructured) bool {
	newFinalizers := newUnstrob.GetFinalizers()
	oldFinalizers := oldUnstrob.GetFinalizers()

	newStatus := newUnstrob.UnstructuredContent()["status"]
	oldStatus := oldUnstrob.UnstructuredContent()["status"]

	return equality.Semantic.DeepEqual(oldFinalizers, newFinalizers) && equality.Semantic.DeepEqual(oldStatus, newStatus)
}

func (c *Controller) process(ctx context.Context, gvr schema.GroupVersionResource, key string) error {
	klog.V(3).InfoS("Processing", "gvr", gvr, "key", key)

	// from downstream
	downstreamNamespace, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("Invalid key: %q: %v", key, err)
		return nil
	}
	downstreamClusterName, name := clusters.SplitClusterAwareKey(clusterAwareName)
	// TODO(sttts): do not reference the cli plugin here
	if strings.HasPrefix(workloadcliplugin.SyncerIDPrefix, downstreamNamespace) {
		// skip syncer namespace
		return nil
	}

	// to upstream
	nsKey := downstreamNamespace
	if !downstreamClusterName.Empty() {
		// If our "physical" cluster is a kcp instance (e.g. for testing purposes), it will return resources
		// with metadata.clusterName set, which means their keys are cluster-aware, so we need to do the same here.
		nsKey = clusters.ToClusterAwareKey(downstreamClusterName, nsKey)
	}
	nsObj, err := c.downstreamNamespaceLister.Get(nsKey)
	if err != nil {
		klog.Errorf("Error retrieving namespace %q from downstream lister: %v", nsKey, err)
		return nil
	}
	nsMeta, ok := nsObj.(metav1.Object)
	if !ok {
		klog.Errorf("Namespace %q expected to be metav1.Object, got %T", nsKey, nsObj)
		return nil
	}
	namespaceLocator, exists, err := shared.LocatorFromAnnotations(nsMeta.GetAnnotations())
	if err != nil {
		klog.Errorf(" namespace %q: error decoding annotation: %v", nsKey, err)
		return nil
	}
	if !exists || namespaceLocator == nil {
		// Only sync resources for the configured logical cluster to ensure
		// that syncers for multiple logical clusters can coexist.
		return nil
	}
	upstreamNamespace := namespaceLocator.Namespace
	upstreamWorkspace := namespaceLocator.Workspace

	// get the downstream object
	obj, exists, err := c.downstreamInformers.ForResource(gvr).Informer().GetIndexer().GetByKey(key)
	if err != nil {
		return err
	}
	if !exists {
		klog.Infof("Downstream GVR %q object %s|%s/%s does not exist. Removing finalizer upstream", gvr.String(), downstreamClusterName, upstreamNamespace, name)
		return shared.EnsureUpstreamFinalizerRemoved(ctx, gvr, c.upstreamInformers, c.upstreamClient, upstreamNamespace, c.syncTargetKey, upstreamWorkspace, name)
	}

	// update upstream status
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("object to synchronize is expected to be Unstructured, but is %T", obj)
	}
	return c.updateStatusInUpstream(ctx, gvr, upstreamNamespace, upstreamWorkspace, u)
}

func (c *Controller) updateStatusInUpstream(ctx context.Context, gvr schema.GroupVersionResource, upstreamNamespace string, upstreamLogicalCluster logicalcluster.Name, downstreamObj *unstructured.Unstructured) error {
	upstreamName := getUpstreamResourceName(downstreamObj)

	downstreamStatus, statusExists, err := unstructured.NestedFieldCopy(downstreamObj.UnstructuredContent(), "status")
	if err != nil {
		return err
	} else if !statusExists {
		klog.V(5).Infof("Resource doesn't contain a status. Skipping updating status of resource %s|%s/%s from syncTargetName namespace %s", upstreamLogicalCluster, upstreamNamespace, upstreamName, downstreamObj.GetNamespace())
		return nil
	}

	existingObj, err := c.upstreamInformers.ForResource(gvr).Lister().ByNamespace(upstreamNamespace).Get(clusters.ToClusterAwareKey(upstreamLogicalCluster, upstreamName))
	if err != nil {
		klog.Errorf("Getting resource %s/%s: %v", upstreamNamespace, upstreamName, err)
		return err
	}

	existing, ok := existingObj.(*unstructured.Unstructured)
	if !ok {
		klog.Errorf("Resource %s|%s/%s expected to be *unstructured.Unstructured, got %T", upstreamLogicalCluster.String(), upstreamNamespace, upstreamName, existing)
		return nil
	}

	newUpstream := existing.DeepCopy()

	if c.advancedSchedulingEnabled {
		statusAnnotationValue, err := json.Marshal(downstreamStatus)
		if err != nil {
			return err
		}
		newUpstreamAnnotations := newUpstream.GetAnnotations()
		if newUpstreamAnnotations == nil {
			newUpstreamAnnotations = make(map[string]string)
		}
		newUpstreamAnnotations[workloadv1alpha1.InternalClusterStatusAnnotationPrefix+c.syncTargetKey] = string(statusAnnotationValue)
		newUpstream.SetAnnotations(newUpstreamAnnotations)

		if reflect.DeepEqual(existing, newUpstream) {
			klog.V(2).Infof("No need to update the status of resource %s|%s/%s from syncTargetName namespace %s", upstreamLogicalCluster, upstreamNamespace, upstreamName, downstreamObj.GetNamespace())
			return nil
		}

		if _, err := c.upstreamClient.Cluster(upstreamLogicalCluster).Resource(gvr).Namespace(upstreamNamespace).Update(ctx, newUpstream, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("Failed updating location status annotation of resource %s|%s/%s from syncTargetName namespace %s: %v", upstreamLogicalCluster, upstreamNamespace, upstreamName, downstreamObj.GetNamespace(), err)
			return err
		}
		klog.Infof("Updated status of resource %s|%s/%s from syncTargetName namespace %s", upstreamLogicalCluster, upstreamNamespace, upstreamName, downstreamObj.GetNamespace())
		return nil
	}

	if err := unstructured.SetNestedField(newUpstream.UnstructuredContent(), downstreamStatus, "status"); err != nil {
		return err
	}

	// TODO (davidfestal): Here in the future we might want to also set some fields of the Spec, per resource type, for example:
	// clusterIP for service, or other field values set by SyncTarget cluster admission.
	// But for now let's only update the status.

	if _, err := c.upstreamClient.Cluster(upstreamLogicalCluster).Resource(gvr).Namespace(upstreamNamespace).UpdateStatus(ctx, newUpstream, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("Failed updating status of resource %q %s|%s/%s from pcluster namespace %s: %v", gvr.String(), upstreamLogicalCluster, upstreamNamespace, upstreamName, downstreamObj.GetNamespace(), err)
		return err
	}
	klog.Infof("Updated status of resource %q %s|%s/%s from pcluster namespace %s", gvr.String(), upstreamLogicalCluster, upstreamNamespace, upstreamName, downstreamObj.GetNamespace())
	return nil
}

// getUpstreamResourceName returns the name with which the resource is known upstream.
func getUpstreamResourceName(downstreamResource *unstructured.Unstructured) string {
	configMapGVR := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}

	if downstreamResource.GroupVersionKind() == configMapGVR && downstreamResource.GetName() == "kcp-root-ca.crt" {
		return "kube-root-ca.crt"
	}
	return downstreamResource.GetName()
}
