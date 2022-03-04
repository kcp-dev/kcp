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
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
)

func deepEqualStatus(oldObj, newObj interface{}) bool {
	oldUnstrob, isOldObjUnstructured := oldObj.(*unstructured.Unstructured)
	newUnstrob, isNewObjUnstructured := newObj.(*unstructured.Unstructured)
	if !isOldObjUnstructured || !isNewObjUnstructured || oldObj == nil || newObj == nil {
		return false
	}

	newStatus := newUnstrob.UnstructuredContent()["status"]
	oldStatus := oldUnstrob.UnstructuredContent()["status"]

	return equality.Semantic.DeepEqual(oldStatus, newStatus)
}

const statusSyncerAgent = "kcp#status-syncer/v0.0.0"

func NewStatusSyncer(from, to *rest.Config, syncedResourceTypes []string, kcpClusterName, pclusterID string) (*Controller, error) {
	from = rest.CopyConfig(from)
	from.UserAgent = statusSyncerAgent
	to = rest.CopyConfig(to)
	to.UserAgent = statusSyncerAgent

	klog.Infof("Creating status syncer for clusterName %s from pcluster %s, resources %v", kcpClusterName, pclusterID, syncedResourceTypes)

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(from)
	if err != nil {
		return nil, err
	}
	fromClient := dynamic.NewForConfigOrDie(from)
	toClients, err := dynamic.NewClusterForConfig(to)
	if err != nil {
		return nil, err
	}
	toClient := toClients.Cluster(kcpClusterName)
	return New(kcpClusterName, pclusterID, discoveryClient, fromClient, toClient, PhysicalClusterToKcp, syncedResourceTypes, pclusterID)
}

func (c *Controller) updateStatusInUpstream(ctx context.Context, gvr schema.GroupVersionResource, upstreamNamespace string, downstreamObj *unstructured.Unstructured) error {
	upstreamStatus := unstructured.Unstructured{}
	upstreamStatus.SetAPIVersion(downstreamObj.GetAPIVersion())
	upstreamStatus.SetKind(downstreamObj.GetKind())
	upstreamStatus.Object["status"] = downstreamObj.Object["status"]

	// Marshalling the unstructured object is good enough as SSA patch
	data, err := json.Marshal(upstreamStatus.Object)
	if err != nil {
		return err
	}

	if _, err := c.toClient.Resource(gvr).Namespace(upstreamNamespace).
		Patch(ctx, downstreamObj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{
			FieldManager: syncerApplyManager,
			Force:        pointer.Bool(true),
		}, "status"); err != nil {
		klog.Errorf("Failed updating status of resource %s|%s/%s from pcluster namespace %s: %v", c.upstreamClusterName, upstreamNamespace, downstreamObj.GetName(), downstreamObj.GetNamespace(), err)
		return err
	}

	return c.updateStatusAnnotationsInUpstream(ctx, gvr, upstreamNamespace, downstreamObj)
}

func (c *Controller) updateStatusAnnotationsInUpstream(ctx context.Context, gvr schema.GroupVersionResource, upstreamNamespace string, downstreamObj *unstructured.Unstructured) error {
	patch := []byte(fmt.Sprintf(`
{
  "metadata":{
    "annotations":{
      %q: %q
    }
  }
}`, targetUIDAnnotation, downstreamObj.GetUID()))

	if _, err := c.toClient.Resource(gvr).Namespace(upstreamNamespace).Patch(ctx, downstreamObj.GetName(), types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		klog.Errorf("Failed updating status annotations of resource %s|%s/%s from pcluster namespace %s: %v", c.upstreamClusterName, upstreamNamespace, downstreamObj.GetName(), downstreamObj.GetNamespace(), err)
		return err
	}

	return nil
}
