/*
n
Copyright 2022 The KCP Authors.

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

package permissionclaimlabel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/informer"
)

const (
	controllerName = "kcp-permissionclaimlabel"
)

// NewController returns a new controller for APIBindings.
func NewController(
	kcpClusterClient kcpclient.Interface,
	dynamicClusterClient dynamic.Interface,
	dynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory,
	apiBindingInformer apisinformers.APIBindingInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue:                queue,
		kcpClusterClient:     kcpClusterClient,
		dynamicClusterClient: dynamicClusterClient,
		ddsif:                dynamicDiscoverySharedInformerFactory,

		apiBindingsLister: apiBindingInformer.Lister(),
		listAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
			list, err := apiBindingInformer.Lister().List(labels.Everything())
			if err != nil {
				return nil, err
			}

			var ret []*apisv1alpha1.APIBinding

			for i := range list {
				if logicalcluster.From(list[i]) != clusterName {
					continue
				}

				ret = append(ret, list[i])
			}

			return ret, nil
		},
		apiBindingsIndexer: apiBindingInformer.Informer().GetIndexer(),
	}

	apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) { c.enqueueAPIBinding(obj, "") },
		UpdateFunc: func(_, newObj interface{}) {
			// TODO: In the future, when we should calculate if there is a need for an update.
			c.enqueueAPIBinding(newObj, "")
		},
		DeleteFunc: func(obj interface{}) { c.enqueueAPIBinding(obj, "") },
	})

	return c, nil

}

// controller reconciles resource labels that make claimed resources visible to an APIExport
// owner. It labels resources in the intersection of `APIBinding.status.permissionClaims` and
// `APIBinding.spec.acceptedPermissionClaims`.
type controller struct {
	queue workqueue.RateLimitingInterface

	kcpClusterClient     kcpclient.Interface
	dynamicClusterClient dynamic.Interface
	ddsif                *informer.DynamicDiscoverySharedInformerFactory

	apiBindingsLister  apislisters.APIBindingLister
	listAPIBindings    func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
	apiBindingsIndexer cache.Indexer
}

// enqueueAPIBinding enqueues an APIBinding .
func (c *controller) enqueueAPIBinding(obj interface{}, logSuffix string) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	klog.V(2).Infof("%v queueing APIBinding %q%s", controllerName, key, logSuffix)
	c.queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Infof("Starting %s controller", controllerName)
	defer klog.Infof("Shutting down %s controller", controllerName)

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

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

func (c *controller) process(ctx context.Context, key string) error {
	_, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("invalid key: %q: %v", key, err)
		return nil
	}
	clusterName, name := clusters.SplitClusterAwareKey(clusterAwareName)

	obj, err := c.apiBindingsLister.Get(key) // TODO: clients need a way to scope down the lister per-cluster
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}
	old := obj
	obj = obj.DeepCopy()

	reconcileErr := c.reconcile(ctx, obj)
	// Regardless of whether reconcile returned an error or not, always try to patch status if needed. Return the
	// reconciliation error at the end.

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(old.Status, obj.Status) {
		oldData, err := json.Marshal(apisv1alpha1.APIBinding{
			Status: old.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for apibinding %s|%s: %w", clusterName, name, err)
		}

		newData, err := json.Marshal(apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				UID:             old.UID,
				ResourceVersion: old.ResourceVersion,
			}, // to ensure they appear in the patch as preconditions
			Status: obj.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for apibinding %s|%s: %w", clusterName, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for apibinding %s|%s: %w", clusterName, name, err)
		}

		klog.V(2).Infof("Patching apibinding %s|%s: %s", clusterName, name, string(patchBytes))
		_, uerr := c.kcpClusterClient.ApisV1alpha1().APIBindings().Patch(logicalcluster.WithCluster(ctx, clusterName), obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		return uerr
	}

	return reconcileErr
}
