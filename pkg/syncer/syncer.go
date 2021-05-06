package syncer

import (
	"context"
	"fmt"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
)

type Controller struct {
	Queue workqueue.RateLimitingInterface

	// Upstream
	FromDSIF dynamicinformer.DynamicSharedInformerFactory

	// Downstream
	ToClient dynamic.Interface
}

type holder struct {
	gvr schema.GroupVersionResource
	obj interface{}
}

func (c *Controller) AddToQueue(gvr schema.GroupVersionResource, obj interface{}) {
	c.Queue.AddRateLimited(holder{gvr: gvr, obj: obj})
}

func (c *Controller) StartWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *Controller) processNextWorkItem() bool {
	// Wait until there is a new item in the working queue
	i, quit := c.Queue.Get()
	if quit {
		return false
	}
	h := i.(holder)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.Queue.Done(i)

	err := c.process(h.gvr, h.obj)
	c.handleErr(err, i)
	return true
}

func (c *Controller) handleErr(err error, i interface{}) {
	// Reconcile worked, nothing else to do for this workqueue item.
	if err == nil {
		c.Queue.Forget(i)
		return
	}

	// Re-enqueue up to 5 times.
	num := c.Queue.NumRequeues(i)
	if num < 5 {
		klog.Errorf("Error reconciling key %q, retrying... (#%d): %v", i, num, err)
		c.Queue.AddRateLimited(i)
		return
	}

	// Give up and report error elsewhere.
	c.Queue.Forget(i)
	utilruntime.HandleError(err)
	klog.Errorf("Dropping key %q after failed retries: %v", i, err)
}

func (c *Controller) process(gvr schema.GroupVersionResource, obj interface{}) error {
	klog.V(2).Infof("Process object of type: %T : %v", obj, obj)
	meta, isMeta := obj.(metav1.Object)
	if !isMeta {
		err := fmt.Errorf("Object to synchronize is expected to be a metav1.Object, but is %T", obj)
		klog.Error(err)
		return err
	}
	namespace, name := meta.GetNamespace(), meta.GetName()

	ctx := context.TODO()

	obj, exists, err := c.FromDSIF.ForResource(gvr).Informer().GetIndexer().Get(obj)
	if err != nil {
		klog.Error(err)
		return err
	}

	if !exists {
		klog.Infof("Object with gvr=%q was deleted : %s/%s", gvr, namespace, name)
		c.delete(ctx, gvr, namespace, name)
		return nil
	}

	unstrob, isUnstructured := obj.(*unstructured.Unstructured)
	if !isUnstructured {
		err := fmt.Errorf("Object to synchronize is expected to be Unstructured, but is %T", obj)
		klog.Error(err)
		return err
	}

	if err := c.ensureNamespaceExists(namespace); err != nil {
		klog.Error(err)
		return err
	}

	if err := c.upsert(ctx, gvr, namespace, unstrob); err != nil {
		return err
	}

	return err
}

func (c *Controller) ensureNamespaceExists(namespace string) error {
	namespaces := c.ToClient.Resource(schema.GroupVersionResource{
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

// getClient gets a dynamic client for the GVR, scoped to namespace if the namespace is not "".
func (c *Controller) getClient(gvr schema.GroupVersionResource, namespace string) dynamic.ResourceInterface {
	nri := c.ToClient.Resource(gvr)
	if namespace != "" {
		return nri.Namespace(namespace)
	}
	return nri
}

func (c *Controller) delete(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
	// TODO: get UID of just-deleted object and pass it as a precondition on this delete.
	// This would avoid races where an object is deleted and another object with the same name is created immediately after.

	return c.getClient(gvr, namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

func (c *Controller) upsert(ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured) error {

	client := c.getClient(gvr, namespace)

	// Attempt to create the object; if the object already exists, update it.

	unstrob.SetUID("")
	unstrob.SetResourceVersion("")
	if _, err := client.Create(ctx, unstrob, metav1.CreateOptions{}); err != nil {
		if !k8serrors.IsAlreadyExists(err) {
			klog.Error(err)
			return err
		}

		existing, err := client.Get(ctx, unstrob.GetName(), metav1.GetOptions{})
		if err != nil {
			klog.Error(err)
			return err
		}
		klog.Infof("Object %s/%s already exists: update it", gvr.Resource, unstrob.GetName())

		unstrob.SetResourceVersion(existing.GetResourceVersion())
		unstrob, err = client.Update(ctx, unstrob, metav1.UpdateOptions{})
		if err != nil {
			klog.Error(err)
			return err
		}
	} else {
		klog.Infof("Created object %s/%s", gvr.Resource, unstrob.GetName())
	}

	klog.Infof("Update the status of object %s/%s", gvr.Resource, unstrob.GetName())
	if _, err := client.UpdateStatus(ctx, unstrob, metav1.UpdateOptions{}); err != nil {
		klog.Error(err)
		return err
	}

	return nil
}
