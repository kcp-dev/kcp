package syncer

import (
	"context"
	"encoding/json"
	"log"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/util/workqueue"
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
		log.Printf("Error reconciling key %q, retrying... (#%d): %v", i, num, err)
		c.Queue.AddRateLimited(i)
		return
	}

	// Give up and report error elsewhere.
	c.Queue.Forget(i)
	utilruntime.HandleError(err)
	log.Printf("Dropping key %q after failed retries: %v", i, err)
}

func (c *Controller) process(gvr schema.GroupVersionResource, obj interface{}) error {
	obj, exists, err := c.FromDSIF.ForResource(gvr).Informer().GetIndexer().Get(obj)
	if err != nil {
		return err
	}

	unstrob, err := interfaceToUnstructured(obj)
	if err != nil {
		return err
	}
	namespace, name := unstrob.GetNamespace(), unstrob.GetName()

	ctx := context.TODO()
	if !exists {
		log.Printf("Object with gvr=%q was deleted", gvr, obj)
		c.delete(ctx, gvr, namespace, name)
		return nil
	}
	if err := c.upsert(ctx, gvr, namespace, unstrob); err != nil {
		return err
	}

	return err
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

	// Attempt to update the object; if the object isn't found, create it.
	unstrob.SetResourceVersion("")
	unstrob.SetUID("")
	if _, err := client.Update(ctx, unstrob, metav1.UpdateOptions{}); err != nil {
		if k8serrors.IsNotFound(err) {
			if _, err := client.Create(ctx, unstrob, metav1.CreateOptions{}); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	_, err := client.UpdateStatus(ctx, unstrob, metav1.UpdateOptions{})
	return err
}

func interfaceToUnstructured(i interface{}) (*unstructured.Unstructured, error) {
	b, err := json.Marshal(i)
	if err != nil {
		return nil, err
	}

	o, _, err := unstructured.UnstructuredJSONScheme.Decode(b, nil, nil)
	if err != nil {
		return nil, err
	}

	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		return nil, err
	}

	return &unstructured.Unstructured{Object: m}, nil
}
