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
	"reflect"

	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

func deepEqualFinalizersAndStatus(oldUnstrob, newUnstrob *unstructured.Unstructured) bool {
	newFinalizers := newUnstrob.GetFinalizers()
	oldFinalizers := oldUnstrob.GetFinalizers()

	newStatus := newUnstrob.UnstructuredContent()["status"]
	oldStatus := oldUnstrob.UnstructuredContent()["status"]

	return equality.Semantic.DeepEqual(oldFinalizers, newFinalizers) && equality.Semantic.DeepEqual(oldStatus, newStatus)
}

func NewStatusSyncer(gvrs []string, kcpClusterName logicalcluster.Name, pclusterID string, advancedSchedulingEnabled bool,
	upstreamClient, downstreamClient dynamic.Interface, upstreamInformers, downstreamInformers dynamicinformer.DynamicSharedInformerFactory) (*Controller, error) {
	return New(kcpClusterName, pclusterID, downstreamClient, upstreamClient, downstreamInformers, SyncUp, gvrs, pclusterID, nil, advancedSchedulingEnabled)
}

func (c *Controller) updateStatusInUpstream(ctx context.Context, gvr schema.GroupVersionResource, upstreamNamespace string, downstreamObj *unstructured.Unstructured) error {
	upstreamObj := downstreamObj.DeepCopy()
	upstreamObj.SetUID("")
	upstreamObj.SetResourceVersion("")
	upstreamObj.SetNamespace(upstreamNamespace)

	// Run name transformations on upstreamObj
	transformName(upstreamObj, SyncUp)

	name := upstreamObj.GetName()
	downstreamStatus, statusExists, err := unstructured.NestedFieldCopy(upstreamObj.UnstructuredContent(), "status")
	if err != nil {
		return err
	} else if !statusExists {
		klog.Infof("Resource doesn't contain a status. Skipping updating status of resource %s|%s/%s from workloadClusterName namespace %s", c.upstreamClusterName, upstreamNamespace, name, downstreamObj.GetNamespace())
		return nil
	}

	existing, err := c.toClient.Resource(gvr).Namespace(upstreamNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		klog.Errorf("Getting resource %s/%s: %v", upstreamNamespace, name, err)
		return err
	}

	// Run any transformations on the object before we update the status on kcp.
	if mutator, ok := c.mutators[gvr]; ok {
		if err := mutator(upstreamObj); err != nil {
			return err
		}
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

		if _, err := c.toClient.Resource(gvr).Namespace(upstreamNamespace).Update(ctx, newUpstream, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("Failed updating location status annotation of resource %s|%s/%s from workloadClusterName namespace %s: %v", c.upstreamClusterName, upstreamNamespace, upstreamObj.GetName(), downstreamObj.GetNamespace(), err)
			return err
		}
		klog.Infof("Updated status of resource %s|%s/%s from workloadClusterName namespace %s", c.upstreamClusterName, upstreamNamespace, upstreamObj.GetName(), downstreamObj.GetNamespace())
		return nil
	}

	if _, err := c.toClient.Resource(gvr).Namespace(upstreamNamespace).UpdateStatus(ctx, upstreamObj, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("Failed updating status of resource %q %s|%s/%s from pcluster namespace %s: %v", gvr.String(), c.upstreamClusterName, upstreamNamespace, upstreamObj.GetName(), downstreamObj.GetNamespace(), err)
		return err
	}
	klog.Infof("Updated status of resource %q %s|%s/%s from pcluster namespace %s", gvr.String(), c.upstreamClusterName, upstreamNamespace, upstreamObj.GetName(), downstreamObj.GetNamespace())
	return nil
}
