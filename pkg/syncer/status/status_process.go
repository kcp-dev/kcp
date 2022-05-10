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
	nsInformer := c.downstreamInformers.ForResource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	nsKey := downstreamNamespace
	if !downstreamClusterName.Empty() {
		// If our "physical" cluster is a kcp instance (e.g. for testing purposes), it will return resources
		// with metadata.clusterName set, which means their keys are cluster-aware, so we need to do the same here.
		nsKey = clusters.ToClusterAwareKey(downstreamClusterName, nsKey)
	}
	nsObj, err := nsInformer.Lister().Get(nsKey)
	if err != nil {
		klog.Errorf("Error retrieving namespace %q from downstream lister: %v", nsKey, err)
		return nil
	}
	nsMeta, ok := nsObj.(metav1.Object)
	if !ok {
		klog.Errorf("Namespace %q expected to be metav1.Object, got %T", nsKey, nsObj)
		return nil
	}
	namespaceLocator, err := shared.LocatorFromAnnotations(nsMeta.GetAnnotations())
	if err != nil {
		klog.Errorf(" namespace %q: error decoding annotation: %v", nsKey, err)
		return nil
	}
	if namespaceLocator == nil || namespaceLocator.LogicalCluster != c.upstreamClusterName {
		// Only sync resources for the configured logical cluster to ensure
		// that syncers for multiple logical clusters can coexist.
		return nil
	}
	upstreamNamespace := namespaceLocator.Namespace

	// get the downstream object
	obj, exists, err := c.downstreamInformers.ForResource(gvr).Informer().GetIndexer().GetByKey(key)
	if err != nil {
		return err
	}
	if !exists {
		if c.advancedSchedulingEnabled {
			// deleted downstream => remove finalizer upstream
			klog.InfoS("Downstream GVR %q object %s|%s/%s does not exist. Removing finalizer upstream", gvr.String(), downstreamClusterName, upstreamNamespace, name)
			return shared.EnsureUpstreamFinalizerRemoved(ctx, gvr, c.upstreamClient, upstreamNamespace, c.workloadClusterName, c.upstreamClusterName, name)
		}
		return nil
	}

	// update upstream status
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("object to synchronize is expected to be Unstructured, but is %T", obj)
	}
	return c.updateStatusInUpstream(ctx, gvr, upstreamNamespace, u)
}

func (c *Controller) updateStatusInUpstream(ctx context.Context, gvr schema.GroupVersionResource, upstreamNamespace string, downstreamObj *unstructured.Unstructured) error {
	upstreamObj := downstreamObj.DeepCopy()
	upstreamObj.SetUID("")
	upstreamObj.SetResourceVersion("")
	upstreamObj.SetNamespace(upstreamNamespace)

	// Run name transformations on upstreamObj
	transformName(upstreamObj)

	name := upstreamObj.GetName()
	downstreamStatus, statusExists, err := unstructured.NestedFieldCopy(upstreamObj.UnstructuredContent(), "status")
	if err != nil {
		return err
	} else if !statusExists {
		klog.Infof("Resource doesn't contain a status. Skipping updating status of resource %s|%s/%s from workloadClusterName namespace %s", c.upstreamClusterName, upstreamNamespace, name, downstreamObj.GetNamespace())
		return nil
	}

	existing, err := c.upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("Getting resource %s/%s: %v", upstreamNamespace, name, err)
		return err
	}

	// TODO: verify that we really only update status, and not some non-status fields in ObjectMeta.
	//       I believe to remember that we had resources where that happened.

	upstreamObj.SetResourceVersion(existing.GetResourceVersion())

	if c.advancedSchedulingEnabled {
		newUpstream := existing.DeepCopy()
		statusAnnotationValue, err := json.Marshal(downstreamStatus)
		if err != nil {
			return err
		}
		newUpstreamAnnotations := newUpstream.GetAnnotations()
		if newUpstreamAnnotations == nil {
			newUpstreamAnnotations = make(map[string]string)
		}
		newUpstreamAnnotations[workloadv1alpha1.InternalClusterStatusAnnotationPrefix+c.workloadClusterName] = string(statusAnnotationValue)
		newUpstream.SetAnnotations(newUpstreamAnnotations)

		if reflect.DeepEqual(existing, newUpstream) {
			klog.V(2).Infof("No need to update the status of resource %s|%s/%s from workloadClusterName namespace %s", c.upstreamClusterName, upstreamNamespace, name, downstreamObj.GetNamespace())
			return nil
		}

		if _, err := c.upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Update(ctx, newUpstream, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("Failed updating location status annotation of resource %s|%s/%s from workloadClusterName namespace %s: %v", c.upstreamClusterName, upstreamNamespace, upstreamObj.GetName(), downstreamObj.GetNamespace(), err)
			return err
		}
		klog.Infof("Updated status of resource %s|%s/%s from workloadClusterName namespace %s", c.upstreamClusterName, upstreamNamespace, upstreamObj.GetName(), downstreamObj.GetNamespace())
		return nil
	}

	if _, err := c.upstreamClient.Resource(gvr).Namespace(upstreamNamespace).UpdateStatus(ctx, upstreamObj, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("Failed updating status of resource %q %s|%s/%s from pcluster namespace %s: %v", gvr.String(), c.upstreamClusterName, upstreamNamespace, upstreamObj.GetName(), downstreamObj.GetNamespace(), err)
		return err
	}
	klog.Infof("Updated status of resource %q %s|%s/%s from pcluster namespace %s", gvr.String(), c.upstreamClusterName, upstreamNamespace, upstreamObj.GetName(), downstreamObj.GetNamespace())
	return nil
}

// TransformName changes the object name into the desired one upstream.
func transformName(syncedObject *unstructured.Unstructured) {
	configMapGVR := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}
	serviceAccountGVR := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ServiceAccount"}

	if syncedObject.GroupVersionKind() == configMapGVR && syncedObject.GetName() == "kcp-root-ca.crt" {
		syncedObject.SetName("kube-root-ca.crt")
	}
	if syncedObject.GroupVersionKind() == serviceAccountGVR && syncedObject.GetName() == "kcp-default" {
		syncedObject.SetName("default")
	}
}
