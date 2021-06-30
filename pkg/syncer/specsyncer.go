package syncer

import (
	"context"

	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
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

func NewSpecSyncer(from, to *rest.Config, syncedResourceTypes []string, clusterID string) (*Controller, error) {
	return New(from, to, upsertIntoDownstream, deleteFromDownstream, func(c *Controller, gvr schema.GroupVersionResource) cache.ResourceEventHandlerFuncs {
		return cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) { c.AddToQueue(gvr, obj) },
			UpdateFunc: func(oldObj, newObj interface{}) {
				if !deepEqualApartFromStatus(oldObj, newObj) {
					c.AddToQueue(gvr, newObj)
				}
			},
			DeleteFunc: func(obj interface{}) { c.AddToQueue(gvr, obj) },
		}
	}, syncedResourceTypes, clusterID)
}

// TODO:
// This function is there as a quick and dirty implementation of namespace creation.
// In fact We should also be getting notifications about namespaces created upstream and be creating downstream equivalents.
func (c *Controller) ensureNamespaceExists(namespace string) error {
	namespaces := c.toClient.Resource(schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "namespaces",
	})
	newNamespace := &unstructured.Unstructured{}
	newNamespace.SetAPIVersion("v1")
	newNamespace.SetKind("Namespace")
	newNamespace.SetName(namespace)
	if _, err := namespaces.Create(context.TODO(), newNamespace, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			klog.Infof("Error while creating namespace %s: %v", namespace, err)
			return err
		}
	}
	return nil
}

func deleteFromDownstream(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	// TODO: get UID of just-deleted object and pass it as a precondition on this delete.
	// This would avoid races where an object is deleted and another object with the same name is created immediately after.

	return c.getClient(gvr, namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

func upsertIntoDownstream(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured) error {
	if err := c.ensureNamespaceExists(namespace); err != nil {
		klog.Error(err)
		return err
	}

	client := c.getClient(gvr, namespace)

	unstrob = unstrob.DeepCopy()

	// Attempt to create the object; if the object already exists, update it.
	unstrob.SetUID("")
	unstrob.SetResourceVersion("")

	ownedByLabel := unstrob.GetLabels()["kcp.dev/owned-by"]
	var ownerReferences []metav1.OwnerReference
	for _, reference := range unstrob.GetOwnerReferences() {
		if reference.Name == ownedByLabel {
			continue
		}
		ownerReferences = append(ownerReferences, reference)
	}
	unstrob.SetOwnerReferences(ownerReferences)

	if _, err := client.Create(ctx, unstrob, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			klog.Errorf("Creating resource %s/%s: %v", namespace, unstrob.GetName(), err)
			return err
		}

		existing, err := client.Get(ctx, unstrob.GetName(), metav1.GetOptions{})
		if err != nil {
			klog.Errorf("Getting resource %s/%s: %v", namespace, unstrob.GetName(), err)
			return err
		}
		klog.Infof("Object %s/%s already exists: update it", gvr.Resource, unstrob.GetName())

		unstrob.SetResourceVersion(existing.GetResourceVersion())
		if _, err := client.Update(ctx, unstrob, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("Updating resource %s/%s: %v", namespace, unstrob.GetName(), err)
			return err
		}
		return nil
	}
	klog.Infof("Created object %s/%s", gvr.Resource, unstrob.GetName())
	return nil
}
