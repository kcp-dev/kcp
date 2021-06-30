/*
Copyright 2021 The Authors

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
	"time"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	typedapiresource "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/apiresource/v1alpha1"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	apiresourcelister "github.com/kcp-dev/kcp/pkg/client/listers/apiresource/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/util/errors"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	typedapiextensions "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	crdexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	crdlister "k8s.io/apiextensions-apiserver/pkg/client/listers/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
)

const resyncPeriod = 10 * time.Hour
const clusterNameAndGVRIndexName = "clusterNameAndGVR"

func GetClusterNameAndGVRIndexKey(clusterName string, gvr metav1.GroupVersionResource) string {
	return clusterName + "$" + gvr.String()
}

func NewController(cfg *rest.Config, autoPublishNegotiatedAPIResource bool) *Controller {
	apiresourceClient := typedapiresource.NewForConfigOrDie(cfg)
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	stopCh := make(chan struct{}) // TODO: hook this up to SIGTERM/SIGINT

	crdClient := typedapiextensions.NewForConfigOrDie(cfg)

	c := &Controller{
		queue:                            queue,
		apiresourceClient:                apiresourceClient,
		crdClient:                        crdClient,
		stopCh:                           stopCh,
		AutoPublishNegotiatedAPIResource: autoPublishNegotiatedAPIResource,
	}

	apiresourceSif := kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpclient.NewForConfigOrDie(cfg), resyncPeriod)
	apiresourceSif.Apiresource().V1alpha1().NegotiatedAPIResources().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(addHandlerAction, nil, obj) },
		UpdateFunc: func(oldObj, obj interface{}) { c.enqueue(updateHandlerAction, oldObj, obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(deleteHandlerAction, nil, obj) },
	})
	c.negotiatedApiResourceIndexer = apiresourceSif.Apiresource().V1alpha1().NegotiatedAPIResources().Informer().GetIndexer()
	c.negotiatedApiResourceIndexer.AddIndexers(map[string]cache.IndexFunc{
		clusterNameAndGVRIndexName: func(obj interface{}) ([]string, error) {
			if negotiatedApiResource, ok := obj.(*apiresourcev1alpha1.NegotiatedAPIResource); ok {
				return []string{GetClusterNameAndGVRIndexKey(negotiatedApiResource.ClusterName, negotiatedApiResource.GVR())}, nil
			}
			return []string{}, nil
		},
	})
	c.negotiatedApiResourceLister = apiresourceSif.Apiresource().V1alpha1().NegotiatedAPIResources().Lister()
	apiresourceSif.Apiresource().V1alpha1().APIResourceImports().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(addHandlerAction, nil, obj) },
		UpdateFunc: func(oldObj, obj interface{}) { c.enqueue(updateHandlerAction, oldObj, obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(deleteHandlerAction, nil, obj) },
	})
	c.apiResourceImportIndexer = apiresourceSif.Apiresource().V1alpha1().APIResourceImports().Informer().GetIndexer()
	c.apiResourceImportIndexer.AddIndexers(map[string]cache.IndexFunc{
		clusterNameAndGVRIndexName: func(obj interface{}) ([]string, error) {
			if apiResourceImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport); ok {
				return []string{GetClusterNameAndGVRIndexKey(apiResourceImport.ClusterName, apiResourceImport.GVR())}, nil
			}
			return []string{}, nil
		},
	})
	c.apiResourceImportLister = apiresourceSif.Apiresource().V1alpha1().APIResourceImports().Lister()

	apiresourceSif.WaitForCacheSync(stopCh)
	apiresourceSif.Start(stopCh)

	crdSif := crdexternalversions.NewSharedInformerFactoryWithOptions(apiextensionsclient.NewForConfigOrDie(cfg), resyncPeriod)
	crdSif.Apiextensions().V1().CustomResourceDefinitions().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(addHandlerAction, nil, obj) },
		UpdateFunc: func(oldObj, obj interface{}) { c.enqueue(updateHandlerAction, oldObj, obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(deleteHandlerAction, nil, obj) },
	})
	c.crdIndexer = crdSif.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer()
	c.crdIndexer.AddIndexers(map[string]cache.IndexFunc{
		clusterNameAndGVRIndexName: func(obj interface{}) ([]string, error) {
			if crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition); ok {
				return []string{GetClusterNameAndGVRIndexKey(crd.ClusterName, metav1.GroupVersionResource{
					Group:    crd.Spec.Group,
					Resource: crd.Spec.Names.Plural,
				})}, nil
			}
			return []string{}, nil
		},
	})
	c.crdLister = crdSif.Apiextensions().V1().CustomResourceDefinitions().Lister()
	crdSif.WaitForCacheSync(stopCh)
	crdSif.Start(stopCh)

	return c
}

type Controller struct {
	queue workqueue.RateLimitingInterface

	apiresourceClient            typedapiresource.ApiresourceV1alpha1Interface
	negotiatedApiResourceIndexer cache.Indexer
	negotiatedApiResourceLister  apiresourcelister.NegotiatedAPIResourceLister
	apiResourceImportIndexer     cache.Indexer
	apiResourceImportLister      apiresourcelister.APIResourceImportLister

	crdClient  typedapiextensions.ApiextensionsV1Interface
	crdIndexer cache.Indexer
	crdLister  crdlister.CustomResourceDefinitionLister

	kubeconfig                       clientcmdapi.Config
	stopCh                           chan struct{}
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
	clusterName   string
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

	c.queue.AddRateLimited(queueElement{
		theAction:     theAction,
		theType:       theType,
		theKey:        key,
		gvr:           gvr,
		clusterName:   newMeta.GetClusterName(),
		deletedObject: deletedObject,
	})
}

func (c *Controller) Start(numThreads int) {
	for i := 0; i < numThreads; i++ {
		go wait.Until(c.startWorker, time.Second, c.stopCh)
	}
	klog.Info("Starting workers")
}

// Stop stops the controller.
func (c *Controller) Stop() {
	klog.Info("Stopping workers")
	c.queue.ShutDown()
	close(c.stopCh)
}

// Done returns a channel that's closed when the controller is stopped.
func (c *Controller) Done() <-chan struct{} { return c.stopCh }

func (c *Controller) startWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *Controller) processNextWorkItem() bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(queueElement)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	err := c.process(key)
	c.handleErr(err, key)
	return true
}

func (c *Controller) handleErr(err error, key queueElement) {
	// Reconcile worked, nothing else to do for this workqueue item.
	if err == nil {
		klog.Info("Successfully reconciled", key)
		c.queue.Forget(key)
		return
	}

	// Re-enqueue up to 5 times.
	num := c.queue.NumRequeues(key)
	if errors.IsRetryable(err) || num < 5 {
		klog.Infof("Error reconciling key %q, retrying... (#%d): %v", key, num, err)
		c.queue.AddRateLimited(key)
		return
	}

	// Give up and report error elsewhere.
	c.queue.Forget(key)
	runtime.HandleError(err)
	klog.Infof("Dropping key %q after failed retries: %v", key, err)
}
