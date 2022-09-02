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

package apiresource

import (
	"context"
	"fmt"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions/apiextensions/v1"
	apiextensionslisters "k8s.io/apiextensions-apiserver/pkg/client/listers/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apiresourceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apiresource/v1alpha1"
	apiresourcelisters "github.com/kcp-dev/kcp/pkg/client/listers/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const clusterNameAndGVRIndexName = "clusterNameAndGVR"
const controllerName = "kcp-apiresource"

func GetClusterNameAndGVRIndexKey(clusterName logicalcluster.Name, gvr metav1.GroupVersionResource) string {
	return clusterName.String() + "$" + gvr.String()
}

func NewController(
	crdClusterClient apiextensionsclient.Interface,
	kcpClusterClient kcpclient.Interface,
	autoPublishNegotiatedAPIResource bool,
	negotiatedAPIResourceInformer apiresourceinformer.NegotiatedAPIResourceInformer,
	apiResourceImportInformer apiresourceinformer.APIResourceImportInformer,
	crdInformer apiextensionsinformers.CustomResourceDefinitionInformer,
) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "kcp-apiresource")

	c := &Controller{
		queue:                            queue,
		crdClusterClient:                 crdClusterClient,
		kcpClusterClient:                 kcpClusterClient,
		AutoPublishNegotiatedAPIResource: autoPublishNegotiatedAPIResource,
		negotiatedApiResourceIndexer:     negotiatedAPIResourceInformer.Informer().GetIndexer(),
		negotiatedApiResourceLister:      negotiatedAPIResourceInformer.Lister(),
		apiResourceImportIndexer:         apiResourceImportInformer.Informer().GetIndexer(),
		apiResourceImportLister:          apiResourceImportInformer.Lister(),
		crdIndexer:                       crdInformer.Informer().GetIndexer(),
		crdLister:                        crdInformer.Lister(),
	}

	negotiatedAPIResourceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(addHandlerAction, nil, obj) },
		UpdateFunc: func(oldObj, obj interface{}) { c.enqueue(updateHandlerAction, oldObj, obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(deleteHandlerAction, nil, obj) },
	})
	if err := c.negotiatedApiResourceIndexer.AddIndexers(map[string]cache.IndexFunc{
		clusterNameAndGVRIndexName: func(obj interface{}) ([]string, error) {
			if negotiatedApiResource, ok := obj.(*apiresourcev1alpha1.NegotiatedAPIResource); ok {
				return []string{GetClusterNameAndGVRIndexKey(logicalcluster.From(negotiatedApiResource), negotiatedApiResource.GVR())}, nil
			}
			return []string{}, nil
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to add indexer for NegotiatedAPIResource: %w", err)
	}

	apiResourceImportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(addHandlerAction, nil, obj) },
		UpdateFunc: func(oldObj, obj interface{}) { c.enqueue(updateHandlerAction, oldObj, obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(deleteHandlerAction, nil, obj) },
	})
	if err := c.apiResourceImportIndexer.AddIndexers(map[string]cache.IndexFunc{
		clusterNameAndGVRIndexName: func(obj interface{}) ([]string, error) {
			if apiResourceImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport); ok {
				return []string{GetClusterNameAndGVRIndexKey(logicalcluster.From(apiResourceImport), apiResourceImport.GVR())}, nil
			}
			return []string{}, nil
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to add indexer for APIResourceImport: %w", err)
	}

	crdInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(addHandlerAction, nil, obj) },
		UpdateFunc: func(oldObj, obj interface{}) { c.enqueue(updateHandlerAction, oldObj, obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(deleteHandlerAction, nil, obj) },
	})
	if err := c.crdIndexer.AddIndexers(map[string]cache.IndexFunc{
		clusterNameAndGVRIndexName: func(obj interface{}) ([]string, error) {
			if crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition); ok {
				return []string{GetClusterNameAndGVRIndexKey(logicalcluster.From(crd), metav1.GroupVersionResource{
					Group:    crd.Spec.Group,
					Resource: crd.Spec.Names.Plural,
				})}, nil
			}
			return []string{}, nil
		},
	}); err != nil {
		return nil, fmt.Errorf("failed to add indexer for CustomResourceDefinition: %w", err)
	}

	return c, nil
}

type Controller struct {
	queue workqueue.RateLimitingInterface

	crdClusterClient             apiextensionsclient.Interface
	kcpClusterClient             kcpclient.Interface
	negotiatedApiResourceIndexer cache.Indexer
	negotiatedApiResourceLister  apiresourcelisters.NegotiatedAPIResourceLister

	apiResourceImportIndexer cache.Indexer
	apiResourceImportLister  apiresourcelisters.APIResourceImportLister

	crdIndexer cache.Indexer
	crdLister  apiextensionslisters.CustomResourceDefinitionLister

	AutoPublishNegotiatedAPIResource bool
}

type queueElementType string

const (
	customResourceDefinitionType queueElementType = "CustomResourceDefinition"
	negotiatedAPIResourceType    queueElementType = "NegotiatedAPIResource"
	apiResourceImportType        queueElementType = "APIResourceImport"
)

type queueElementAction string

const (
	specChangedAction             queueElementAction = "SpecChanged"
	statusOnlyChangedAction       queueElementAction = "StatusOnlyChanged"
	annotationOrLabelsOnlyChanged queueElementAction = "AnnotationOrLabelsOnlyChanged"
	deletedAction                 queueElementAction = "Deleted"
	createdAction                 queueElementAction = "Created"
)

type resourceHandlerAction string

const (
	addHandlerAction    resourceHandlerAction = "Add"
	updateHandlerAction resourceHandlerAction = "Update"
	deleteHandlerAction resourceHandlerAction = "Delete"
)

type queueElement struct {
	theAction     queueElementAction
	theType       queueElementType
	theKey        string
	gvr           metav1.GroupVersionResource
	clusterName   logicalcluster.Name
	deletedObject interface{}
}

func toQueueElementType(oldObj, obj interface{}) (theType queueElementType, gvr metav1.GroupVersionResource, oldMeta, newMeta metav1.Object, oldStatus, newStatus interface{}) {
	switch typedObj := obj.(type) {
	case *apiextensionsv1.CustomResourceDefinition:
		theType = customResourceDefinitionType
		newMeta = typedObj
		newStatus = typedObj.Status
		if oldObj != nil {
			typedOldObj := oldObj.(*apiextensionsv1.CustomResourceDefinition)
			oldStatus = typedOldObj.Status
			oldMeta = typedOldObj
		}
		gvr = metav1.GroupVersionResource{
			Group:    typedObj.Spec.Group,
			Resource: typedObj.Spec.Names.Plural,
		}
	case *apiresourcev1alpha1.APIResourceImport:
		theType = apiResourceImportType
		newMeta = typedObj
		newStatus = typedObj.Status
		if oldObj != nil {
			typedOldObj := oldObj.(*apiresourcev1alpha1.APIResourceImport)
			oldStatus = typedOldObj.Status
			oldMeta = typedOldObj
		}
		gvr = metav1.GroupVersionResource{
			Group:    typedObj.Spec.GroupVersion.Group,
			Version:  typedObj.Spec.GroupVersion.Version,
			Resource: typedObj.Spec.Plural,
		}
	case *apiresourcev1alpha1.NegotiatedAPIResource:
		theType = negotiatedAPIResourceType
		newMeta = typedObj
		newStatus = typedObj.Status
		if oldObj != nil {
			typedOldObj := oldObj.(*apiresourcev1alpha1.NegotiatedAPIResource)
			oldStatus = typedOldObj.Status
			oldMeta = typedOldObj
		}
		gvr = metav1.GroupVersionResource{
			Group:    typedObj.Spec.GroupVersion.Group,
			Version:  typedObj.Spec.GroupVersion.Version,
			Resource: typedObj.Spec.Plural,
		}
	case cache.DeletedFinalStateUnknown:
		tombstone := typedObj
		theType, gvr, oldMeta, newMeta, oldStatus, newStatus = toQueueElementType(nil, tombstone.Obj)
		if theType == "" {
			klog.Errorf("Tombstone contained object that is not expected %#v", obj)
		}
	}
	return
}

func (c *Controller) enqueue(action resourceHandlerAction, oldObj, obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	if obj == nil {
		return
	}

	theType, gvr, oldMeta, newMeta, oldStatus, newStatus := toQueueElementType(oldObj, obj)
	var theAction queueElementAction
	var deletedObject interface{}

	switch action {
	case "Add":
		theAction = createdAction
	case "Update":
		if oldMeta == nil {
			theAction = createdAction
			break
		}

		if oldMeta.GetResourceVersion() == newMeta.GetResourceVersion() {
			return
		}

		if oldMeta.GetGeneration() != newMeta.GetGeneration() {
			theAction = specChangedAction
			break
		}

		if !equality.Semantic.DeepEqual(oldStatus, newStatus) {
			theAction = statusOnlyChangedAction
			break
		}

		if !equality.Semantic.DeepEqual(oldMeta.GetAnnotations(), newMeta.GetAnnotations()) ||
			equality.Semantic.DeepEqual(oldMeta.GetLabels(), newMeta.GetLabels()) {
			theAction = annotationOrLabelsOnlyChanged
			break
		}
		// Nothing significant changed. Ignore the event.
		return
	case "Delete":
		theAction = deletedAction
		deletedObject = obj
	}

	c.queue.Add(queueElement{
		theAction:     theAction,
		theType:       theType,
		theKey:        key,
		gvr:           gvr,
		clusterName:   logicalcluster.From(newMeta),
		deletedObject: deletedObject,
	})
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startWorker(ctx) }, time.Second, ctx.Done())
	}

	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(queueElement)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key.theKey).WithValues(
		"action", key.theAction,
		"type", key.theType,
		"gvr", key.gvr,
		"clusterName", key.clusterName,
	)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %v, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}
