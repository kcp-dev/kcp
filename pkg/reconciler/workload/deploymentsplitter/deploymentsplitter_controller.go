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

package deploymentsplitter

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubernetesinformers "k8s.io/client-go/informers"
	kubernetesclient "k8s.io/client-go/kubernetes"
	appsv1client "k8s.io/client-go/kubernetes/typed/apps/v1"
	appsv1lister "k8s.io/client-go/listers/apps/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	workloadlisters "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const resyncPeriod = 10 * time.Hour
const controllerName = "deployment"

// NewController returns a new Controller which splits new Deployment objects
// into N virtual Deployments labeled for each Cluster that exists at the time
// the Deployment is created.
func NewController(cfg *rest.Config) *Controller {
	client := appsv1client.NewForConfigOrDie(cfg)
	kubeClient := kubernetesclient.NewForConfigOrDie(cfg)
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "kcp-deployment")
	stop := context.TODO()
	stop, _ = signal.NotifyContext(stop, os.Interrupt)

	csif := kcpinformers.NewSharedInformerFactoryWithOptions(kcpclient.NewForConfigOrDie(cfg), resyncPeriod)

	c := &Controller{
		queue:         queue,
		client:        client,
		clusterLister: csif.Workload().V1alpha1().SyncTargets().Lister(),
		kubeClient:    kubeClient,
		stop:          stop,
	}
	csif.WaitForCacheSync(stop.Done())
	csif.Start(stop.Done())

	sif := kubernetesinformers.NewSharedInformerFactoryWithOptions(kubeClient, resyncPeriod)
	sif.Apps().V1().Deployments().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})
	sif.WaitForCacheSync(stop.Done())
	sif.Start(stop.Done())

	c.indexer = sif.Apps().V1().Deployments().Informer().GetIndexer()
	c.lister = sif.Apps().V1().Deployments().Lister()

	return c
}

type Controller struct {
	queue         workqueue.RateLimitingInterface
	client        *appsv1client.AppsV1Client
	clusterLister workloadlisters.SyncTargetLister
	kubeClient    kubernetesclient.Interface
	stop          context.Context
	indexer       cache.Indexer
	lister        appsv1lister.DeploymentLister
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	c.queue.Add(key)
}

func (c *Controller) Start(numThreads int) {
	defer c.queue.ShutDown()
	logger := logging.WithReconciler(klog.FromContext(c.stop), controllerName)
	c.stop = klog.NewContext(c.stop, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(c.stop, c.startWorker, time.Second)
	}
	<-c.stop.Done()
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
	key := k.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

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
	logger := klog.FromContext(ctx)
	obj, exists, err := c.indexer.GetByKey(key)
	if err != nil {
		return err
	}

	if !exists {
		logger.Info("object was deleted")
		return nil
	}
	current := obj.(*appsv1.Deployment).DeepCopy()
	previous := current.DeepCopy()

	logger = logging.WithObject(logger, previous)
	ctx = klog.NewContext(ctx, logger)

	if err := c.reconcile(ctx, current); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(previous, current) {
		_, uerr := c.client.Deployments(current.Namespace).Update(ctx, current, metav1.UpdateOptions{})
		return uerr
	}

	return err
}
