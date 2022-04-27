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

package syncer

import (
	"context"
	"encoding/json"
	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

func deepEqualStatus(oldObj, newObj interface{}) bool {
	oldUnstrob, isOldObjUnstructured := oldObj.(*unstructured.Unstructured)
	newUnstrob, isNewObjUnstructured := newObj.(*unstructured.Unstructured)
	if !isOldObjUnstructured || !isNewObjUnstructured || oldObj == nil || newObj == nil {
		return false
	}

	newFinalizers := newUnstrob.GetFinalizers()
	oldFinalizers := oldUnstrob.GetFinalizers()

	newStatus := newUnstrob.UnstructuredContent()["status"]
	oldStatus := oldUnstrob.UnstructuredContent()["status"]

	return equality.Semantic.DeepEqual(oldFinalizers, newFinalizers) && equality.Semantic.DeepEqual(oldStatus, newStatus)
}

const statusSyncerAgent = "kcp#status-syncer/v0.0.0"

func NewStatusSyncer(from, to *rest.Config, gvrs []string, kcpClusterName logicalcluster.LogicalCluster, pclusterID string) (*Controller, error) {
	from = rest.CopyConfig(from)
	from.UserAgent = statusSyncerAgent
	to = rest.CopyConfig(to)
	to.UserAgent = statusSyncerAgent

	fromClient := dynamic.NewForConfigOrDie(from)
	toClients, err := dynamic.NewClusterForConfig(to)
	if err != nil {
		return nil, err
	}
	toClient := toClients.Cluster(kcpClusterName)

	// Register the default mutators
	mutatorsMap := getDefaultMutators(from)

	return New(kcpClusterName, pclusterID, fromClient, toClient, SyncUp, gvrs, pclusterID, mutatorsMap)
}

func (c *Controller) deleteFromUpstream(ctx context.Context, gvr schema.GroupVersionResource, upstreamNamespace, name string) error {
	return c.removeFinalizersAndUpdate(ctx, c.toClient, gvr, upstreamNamespace, name)
}

// removeFinalizersAndUpdate cleans the object from finalizers, the cluster label, and the status annotation, then updates the object upstream.
func (c *Controller) removeFinalizersAndUpdate(ctx context.Context, upstreamClient dynamic.Interface, gvr schema.GroupVersionResource, upstreamNamespace, name string) error {
	upstreamObj, err := upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if errors.IsNotFound(err) {
		return nil
	}

	upstreamLabels := upstreamObj.GetLabels()

	// Check if the upstream object still has the deletion timestamp or the location annotation for deletion.
	if upstreamObj.GetAnnotations()[LocationDeletionAnnotationName(c.pcluster)] == "" && upstreamObj.GetDeletionTimestamp() == nil {
		// Do nothing: the object should not be deleted anymore for this location on the KCP side
		return nil
	}

	// Let's remove the Syncer finalizer from the upstreamObj
	c.deleteSyncerFinalizer(upstreamObj)

	// remove the cluster label.
	delete(upstreamLabels, workloadClusterLabelName(c.pcluster))
	upstreamObj.SetLabels(upstreamLabels)

	// Clean up the status annotation and the locationDeletionAnnotation
	annotations := upstreamObj.GetAnnotations()
	delete(annotations, statusAnnotationName(c.pcluster))
	delete(annotations, LocationDeletionAnnotationName(c.pcluster))
	upstreamObj.SetAnnotations(annotations)

	if _, err := upstreamClient.Resource(gvr).Namespace(upstreamObj.GetNamespace()).Update(ctx, upstreamObj, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("Failed updating after removing the finalizers of resource %s|%s/%s: %v", c.upstreamClusterName, upstreamNamespace, upstreamObj.GetName(), err)
		return err
	}
	klog.Infof("Updated resource %s|%s/%s after removing the finalizers", c.upstreamClusterName, upstreamNamespace, upstreamObj.GetName())
	return nil
}

func (c *Controller) updateStatusInUpstream(ctx context.Context, eventType watch.EventType, gvr schema.GroupVersionResource, upstreamNamespace string, downstreamObj *unstructured.Unstructured) error {
	upstreamObj := downstreamObj.DeepCopy()
	upstreamObj.SetUID("")
	upstreamObj.SetResourceVersion("")
	upstreamObj.SetNamespace(upstreamNamespace)

	// Run name transformations on upstreamObj
	transformName(upstreamObj, SyncUp)

	name := upstreamObj.GetName()
	if _, statusExists, err := unstructured.NestedFieldCopy(upstreamObj.UnstructuredContent(), "status"); err != nil {
		return err
	} else if !statusExists {
		klog.Infof("Resource doesn't contain a status. Skipping updating status of resource %s|%s/%s from pcluster namespace %s", c.upstreamClusterName, upstreamNamespace, name, downstreamObj.GetNamespace())
		return nil
	}

	existing, err := c.toClient.Resource(gvr).Namespace(upstreamNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("Getting resource %s/%s: %v", upstreamNamespace, name, err)
		return err
	}

	// TODO(jmprusi): Right now, this is totally ignored as we reuse the "Existing" object to avoid updating Spec fields
	//				  from downstream when updating the wanted annotations/labels to upstream pushing, this will be removed
	//                when we have the virtual syncer workspace.
	// Run any transformations on the object before we update the status on kcp.
	if mutator, ok := c.mutators[gvr]; ok {
		if err := mutator(upstreamObj); err != nil {
			return err
		}
	}

	status, _, err := unstructured.NestedFieldCopy(upstreamObj.UnstructuredContent(), "status")
	if err != nil {
		return err
	}

	statusJsonBytes, err := json.Marshal(status)
	if err != nil {
		return err
	}

	annotations := existing.GetAnnotations()
	// Update the annotation with the serialized status from downstream.
	annotations[statusAnnotationName(c.pcluster)] = string(statusJsonBytes)
	existing.SetAnnotations(annotations)

	// TODO(jmprusi): Right now we are updating the whole object. This needs to be a UpdateStatus call once we have the virtual syncer workspaces
	if _, err := c.toClient.Resource(gvr).Namespace(upstreamNamespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("Failed updating status annotation of resource %s|%s/%s from pcluster namespace %s: %v", c.upstreamClusterName, upstreamNamespace, upstreamObj.GetName(), downstreamObj.GetNamespace(), err)
		return err
	}
	klog.Infof("Updated status annotation of resource %s|%s/%s from pcluster namespace %s", c.upstreamClusterName, upstreamNamespace, upstreamObj.GetName(), downstreamObj.GetNamespace())
	return nil
}
