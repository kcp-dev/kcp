package syncer

import (
	"context"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"
)


func NewStatusSyncer(from, to *rest.Config, syncedResourceTypes []string, clusterID string) (*Controller, error, bool) {
	return New(from, to, updateStatusInUpstream, nil, func(c *Controller, gvr schema.GroupVersionResource) cache.ResourceEventHandlerFuncs{
		return cache.ResourceEventHandlerFuncs{
			UpdateFunc: func(oldObj, obj interface{}) {
				oldUnstrob, isOldObjUnstructured := oldObj.(*unstructured.Unstructured)
				newUnstrob, isNewObjUnstructured := obj.(*unstructured.Unstructured)
				if isOldObjUnstructured && isNewObjUnstructured && oldUnstrob != nil && newUnstrob != nil {
					if newStatus, statusExists := newUnstrob.UnstructuredContent()["status"]; statusExists {
						oldStatus := oldUnstrob.UnstructuredContent()["status"]
						areEqual := equality.Semantic.DeepEqual(oldStatus, newStatus)
						if ! areEqual {
							c.AddToQueue(gvr, obj)
						}
					}
				}
			},
		}
	}, syncedResourceTypes, clusterID)
}

func updateStatusInUpstream(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured) error {
	client := c.getClient(gvr, namespace)

	unstrob = unstrob.DeepCopy()

	// Attempt to create the object; if the object already exists, update it.
	unstrob.SetUID("")
	unstrob.SetResourceVersion("")

	existing, err := client.Get(ctx, unstrob.GetName(), metav1.GetOptions{})
	if err != nil {
		klog.Errorf("Getting resource %s/%s: %v", namespace, unstrob.GetName(), err)
		return err
	}

	unstrob.SetResourceVersion(existing.GetResourceVersion())
	if _, err := client.UpdateStatus(ctx, unstrob, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("Updating status of resource %s/%s: %v", namespace, unstrob.GetName(), err)
		return err
	}

	return nil
}
