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

	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
)

func deepEqualApartFromStatus(oldObj, newObj interface{}) bool {
	oldUnstrob, isOldObjUnstructured := oldObj.(*unstructured.Unstructured)
	newUnstrob, isNewObjUnstructured := newObj.(*unstructured.Unstructured)
	if !isOldObjUnstructured || !isNewObjUnstructured {
		return false
	}
	if !equality.Semantic.DeepEqual(oldUnstrob.GetAnnotations(), newUnstrob.GetAnnotations()) {
		return false
	}
	if !equality.Semantic.DeepEqual(oldUnstrob.GetLabels(), newUnstrob.GetLabels()) {
		return false
	}

	oldObjKeys := sets.StringKeySet(oldUnstrob.UnstructuredContent())
	newObjKeys := sets.StringKeySet(newUnstrob.UnstructuredContent())
	for _, key := range oldObjKeys.Union(newObjKeys).UnsortedList() {
		if key == "metadata" || key == "status" {
			continue
		}
		if !equality.Semantic.DeepEqual(oldUnstrob.UnstructuredContent()[key], newUnstrob.UnstructuredContent()[key]) {
			return false
		}
	}
	return true
}

const specSyncerAgent = "kcp#spec-syncer/v0.0.0"

func NewSpecSyncer(from, to *rest.Config, syncedResourceTypes []string, clusterID, logicalClusterID string) (*Controller, error) {
	from = rest.CopyConfig(from)
	from.UserAgent = specSyncerAgent
	to = rest.CopyConfig(to)
	to.UserAgent = specSyncerAgent

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(from)
	if err != nil {
		return nil, err
	}
	fromDiscovery := discoveryClient.WithCluster(logicalClusterID)
	fromClients, err := dynamic.NewClusterForConfig(from)
	if err != nil {
		return nil, err
	}
	fromClient := fromClients.Cluster(logicalClusterID)
	toClient := dynamic.NewForConfigOrDie(to)
	return New(fromDiscovery, fromClient, toClient, KcpToPhysicalCluster, syncedResourceTypes, clusterID)
}

func (c *Controller) deleteFromDownstream(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	// TODO: get UID of just-deleted object and pass it as a precondition on this delete.
	// This would avoid races where an object is deleted and another object with the same name is created immediately after.

	return c.toClient.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

// TODO: This function is there as a quick and dirty implementation of namespace creation.
//       In fact We should also be getting notifications about namespaces created upstream and be creating downstream equivalents.
func (c *Controller) ensureNamespaceExists(ctx context.Context, namespace string) error {
	namespaces := c.toClient.Resource(schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "namespaces",
	})

	// Not found - try to create
	newNamespace := &unstructured.Unstructured{}
	newNamespace.SetAPIVersion("v1")
	newNamespace.SetKind("Namespace")
	newNamespace.SetName(namespace)
	if _, err := namespaces.Create(ctx, newNamespace, metav1.CreateOptions{}); err != nil {
		// An already exists error is ok - it means something else beat us to creating the namespace.
		if !k8serrors.IsAlreadyExists(err) {
			// Any other error is not good, though.
			// TODO bubble this up as a condition somewhere.
			klog.Errorf("Error while creating namespace %q: %v", namespace, err)
			return err
		}
	}

	return nil
}

func (c *Controller) applyToDownstream(ctx context.Context, gvr schema.GroupVersionResource, namespace string, obj *unstructured.Unstructured) error {
	if err := c.ensureNamespaceExists(ctx, namespace); err != nil {
		return err
	}

	obj = obj.DeepCopy()
	obj.SetUID("")
	obj.SetResourceVersion("")
	obj.SetNamespace(namespace)
	obj.SetManagedFields(nil)

	ownedByLabel := obj.GetLabels()["kcp.dev/owned-by"]
	var ownerReferences []metav1.OwnerReference
	for _, reference := range obj.GetOwnerReferences() {
		if reference.Name == ownedByLabel {
			continue
		}
		ownerReferences = append(ownerReferences, reference)
	}
	obj.SetOwnerReferences(ownerReferences)

	// TODO: wipe things like finalizers, owner-refs and any other life-cycle fields. The life-cycle
	//       should exclusively owned by the syncer. Let's not some Kubernetes magic interfere with it.

	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}

	if _, err := c.toClient.Resource(gvr).Namespace(namespace).Patch(ctx, obj.GetName(), types.ApplyPatchType, data, metav1.PatchOptions{FieldManager: syncerApplyManager, Force: pointer.Bool(true)}); err != nil {
		klog.Errorf("Applying resource %s/%s to cluster %q: %v", namespace, obj.GetName(), obj.GetClusterName(), err)
		return err
	}
	klog.Infof("Applied object %s/%s to cluster %q", gvr.Resource, obj.GetName(), obj.GetClusterName())

	return nil
}
