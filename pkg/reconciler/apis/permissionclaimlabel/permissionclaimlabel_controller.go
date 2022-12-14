/*
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
	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	apisv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	apisv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerName = "kcp-permissionclaimlabel"
)

// NewController returns a new controller for handling permission claims for an APIBinding.
// it will own the AppliedPermissionClaims and will own the accepted permission claim condition.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	dynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory,
	apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
	apiExportInformer apisv1alpha1informers.APIExportClusterInformer,
) (*controller, error) {
	logger := logging.WithReconciler(klog.Background(), ControllerName)

	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &controller{
		queue:                queue,
		kcpClusterClient:     kcpClusterClient,
		dynamicClusterClient: dynamicClusterClient,
		ddsif:                dynamicDiscoverySharedInformerFactory,

		apiBindingsLister:  apiBindingInformer.Lister(),
		apiBindingsIndexer: apiBindingInformer.Informer().GetIndexer(),

		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
			objs, err := apiExportInformer.Informer().GetIndexer().ByIndex(indexers.ByLogicalClusterPathAndName, path.Join(name).String())
			if err != nil {
				return nil, err
			}
			if len(objs) == 0 {
				return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiexports"), path.Join(name).String())
			}
			if len(objs) > 1 {
				return nil, fmt.Errorf("multiple APIExports found for %s", path.Join(name).String())
			}
			return objs[0].(*apisv1alpha1.APIExport), nil
		},
	}

	indexers.AddIfNotPresentOrDie(apiExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) { c.enqueueAPIBinding(obj, logger) },
		UpdateFunc: func(_, newObj interface{}) {
			c.enqueueAPIBinding(newObj, logger)
		},
		DeleteFunc: func(obj interface{}) { c.enqueueAPIBinding(obj, logger) },
	})

	return c, nil

}

// controller reconciles resource labels that make claimed resources visible to an APIExport
// owner. It labels resources in the intersection of `APIBinding.status.permissionClaims` and
// `APIBinding.spec.acceptedPermissionClaims`.
type controller struct {
	queue workqueue.RateLimitingInterface

	kcpClusterClient     kcpclientset.ClusterInterface
	apiBindingsIndexer   cache.Indexer
	dynamicClusterClient kcpdynamic.ClusterInterface
	ddsif                *informer.DynamicDiscoverySharedInformerFactory

	apiBindingsLister apisv1alpha1listers.APIBindingClusterLister
	getAPIExport      func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)
}

// enqueueAPIBinding enqueues an APIBinding.
func (c *controller) enqueueAPIBinding(obj interface{}, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing APIBinding")
	c.queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("starting controller")
	defer logger.Info("shutting down controller")

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

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "invalid key")
		return nil
	}

	obj, err := c.apiBindingsLister.Cluster(clusterName).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}

	logger = logging.WithObject(logger, obj)
	ctx = klog.NewContext(ctx, logger)

	old := obj
	obj = obj.DeepCopy()

	var errs []error
	if err := c.reconcile(ctx, obj); err != nil {
		errs = append(errs, err)
	}

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

		logger.V(2).Info("patching APIBinding", "patch", string(patchBytes))
		if _, err := c.kcpClusterClient.Cluster(clusterName.Path()).ApisV1alpha1().APIBindings().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status"); err != nil {
			errs = append(errs, err)

		}
	}

	return utilerrors.NewAggregate(errs)
}
