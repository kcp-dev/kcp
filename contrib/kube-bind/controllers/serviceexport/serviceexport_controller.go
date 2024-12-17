/*
Copyright 2022 The Kube Bind Authors.

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

package serviceexport

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	apiextensionsinformers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	apiextensionslisters "github.com/kcp-dev/client-go/apiextensions/listers/apiextensions/v1"
	"github.com/kcp-dev/kcp/contrib/kube-bind/committer"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kube-bind/kube-bind/pkg/indexers"
	kubebindv1alpha1 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha1"
	kubebindv1alpha1clusterclient "github.com/kube-bind/kube-bind/sdk/kcp/clientset/versioned/cluster/typed/kubebind/v1alpha1"

	bindinformers "github.com/kube-bind/kube-bind/sdk/kcp/informers/externalversions/kubebind/v1alpha1"
	bindlisters "github.com/kube-bind/kube-bind/sdk/kcp/listers/kubebind/v1alpha1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const (
	controllerName = "kube-bind-example-backend-serviceexport"
)

// NewController returns a new controller to reconcile ServiceExports.
func NewController(
	config *rest.Config,
	serviceExportInformer bindinformers.APIServiceExportClusterInformer,
	crdInformer apiextensionsinformers.CustomResourceDefinitionClusterInformer,
) (*Controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	logger := klog.Background().WithValues("controller", controllerName)

	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, controllerName)

	bindClient, err := kubebindv1alpha1clusterclient.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	c := &Controller{
		queue: queue,

		bindClient: bindClient,

		serviceExportLister:  serviceExportInformer.Lister(),
		serviceExportIndexer: serviceExportInformer.Informer().GetIndexer(),

		crdLister:  crdInformer.Lister(),
		crdIndexer: crdInformer.Informer().GetIndexer(),

		reconciler: reconciler{
			getCRD: func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
				return crdInformer.Lister().Cluster(clusterName).Get(name)
			},
			deleteServiceExport: func(ctx context.Context, clusterName logicalcluster.Name, ns, name string) error {
				return bindClient.Cluster(clusterName.Path()).APIServiceExports(ns).Delete(ctx, name, metav1.DeleteOptions{})
			},
			requeue: func(export *kubebindv1alpha1.APIServiceExport) {
				key, err := kcpcache.MetaClusterNamespaceKeyFunc(export)
				if err != nil {
					runtime.HandleError(err)
					return
				}
				queue.Add(key)
			},
		},
	}

	indexers.AddIfNotPresentOrDie(serviceExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ServiceExportByCustomResourceDefinition: indexers.IndexServiceExportByCustomResourceDefinition,
	})

	serviceExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueServiceExport(logger, obj)
		},
		UpdateFunc: func(old, newObj interface{}) {
			c.enqueueServiceExport(logger, newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueServiceExport(logger, obj)
		},
	})

	crdInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueCRD(logger, obj)
		},
		UpdateFunc: func(old, newObj interface{}) {
			c.enqueueCRD(logger, newObj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueCRD(logger, obj)
		},
	})

	return c, nil
}

type Resource = committer.Resource[*kubebindv1alpha1.APIServiceExportSpec, *kubebindv1alpha1.APIServiceExportStatus]

// Controller reconciles ServiceNamespaces by creating a Namespace for each, and deleting it if
// the APIServiceNamespace is deleted.
type Controller struct {
	queue workqueue.RateLimitingInterface

	serviceExportLister  bindlisters.APIServiceExportClusterLister
	serviceExportIndexer cache.Indexer

	crdLister  apiextensionslisters.CustomResourceDefinitionClusterLister
	crdIndexer cache.Indexer

	reconciler

	bindClient *kubebindv1alpha1clusterclient.KubeBindV1alpha1ClusterClient
}

func (c *Controller) enqueueServiceExport(logger klog.Logger, obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger.V(2).Info("queueing APIServiceExport", "key", key)
	c.queue.Add(key)
}

func (c *Controller) enqueueCRD(logger klog.Logger, obj interface{}) {
	crdKey, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	exports, err := c.serviceExportIndexer.ByIndex(indexers.ServiceExportByCustomResourceDefinition, crdKey)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, obj := range exports {
		export, ok := obj.(*kubebindv1alpha1.APIServiceExport)
		if !ok {
			runtime.HandleError(fmt.Errorf("unexpected type %T", obj))
			return
		}
		key, err := cache.MetaNamespaceKeyFunc(export)
		if err != nil {
			runtime.HandleError(err)
			continue
		}
		logger.V(2).Info("queueing APIServiceExport", "key", key, "reason", "CustomResourceDefinition", "CustomResourceDefinitionKey", crdKey)
		c.queue.Add(key)
	}
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := klog.FromContext(ctx).WithValues("controller", controllerName)

	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
	defer runtime.HandleCrash()

	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	logger := klog.FromContext(ctx).WithValues("key", key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(2).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	clusterName, snsNamespace, snsName, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return nil
	}

	obj, err := c.serviceExportLister.Cluster(clusterName).APIServiceExports(snsNamespace).Get(snsName)
	if err != nil && !errors.IsNotFound(err) {
		return err
	} else if errors.IsNotFound(err) {
		return nil // nothing we can do
	}

	old := obj
	obj = obj.DeepCopy()

	var errs []error
	if err := c.reconcile(ctx, clusterName, obj); err != nil {
		errs = append(errs, err)
	}

	// Regardless of whether reconcile returned an error or not, always try to patch status if needed. Return the
	// reconciliation error at the end.

	// If the object being reconciled changed as a result, update it.
	objectMetaChanged := !equality.Semantic.DeepEqual(old.ObjectMeta, obj.ObjectMeta)
	specChanged := !equality.Semantic.DeepEqual(old.Spec, obj.Spec)
	statusChanged := !equality.Semantic.DeepEqual(old.Status, obj.Status)

	specOrObjectMetaChanged := specChanged || objectMetaChanged

	// Simultaneous updates of spec and status are never allowed.
	if specOrObjectMetaChanged && statusChanged {
		panic(fmt.Sprintf("programmer error: spec and status changed in same reconcile iteration. diff=%s", cmp.Diff(old, obj)))
	}

	oldResource := &Resource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &Resource{ObjectMeta: obj.ObjectMeta, Spec: &obj.Spec, Status: &obj.Status}

	patchBytes, subresources, err := committer.GeneratePatchAndSubResources(oldResource, newResource)
	if err != nil {
		errs = append(errs, err)
	}

	if len(patchBytes) == 0 {
		return nil
	}

	_, err = c.bindClient.Cluster(clusterName.Path()).APIServiceExports(snsNamespace).Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, subresources...)
	return err
}
