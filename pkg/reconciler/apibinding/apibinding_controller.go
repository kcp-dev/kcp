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

package apibinding

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"

	apiextensionclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
)

const (
	controllerName      = "kcp-apibinding"
	byWorkspaceExport   = "byWorkspaceExport"
	shadowWorkspaceName = "system:bound-crds"
)

func NewController(
	crdClient apiextensionclientset.ClusterInterface,
	kcpClient kcpclient.ClusterInterface,
	//workspaceInformer tenancyinformer.ClusterWorkspaceInformer,
	apiBindingInformer apisinformers.APIBindingInformer,
	apiExportInformer apisinformers.APIExportInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue:            queue,
		crdClusterClient: crdClient,
		kcpClusterClient: kcpClient,
		//workspaceLister:    workspaceInformer.Lister(),
		apiBindingsLister:  apiBindingInformer.Lister(),
		apiBindingsIndexer: apiBindingInformer.Informer().GetIndexer(),
		apiExportsLister:   apiExportInformer.Lister(),
	}

	apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueue(obj) },
	})

	if err := apiBindingInformer.Informer().AddIndexers(cache.Indexers{
		byWorkspaceExport: func(obj interface{}) ([]string, error) {
			apiBinding, ok := obj.(*apisv1alpha1.APIBinding)
			if !ok {
				return []string{}, fmt.Errorf("obj is supposed to be an APIBinding, but is %T", obj)
			}

			if apiBinding.Spec.Reference.Workspace != nil {
				org, _, err := helper.ParseLogicalClusterName(apiBinding.ClusterName)
				if err != nil {
					return []string{}, fmt.Errorf("error parsing clusterName %q: %w", apiBinding.ClusterName, err)
				}
				apiExportClusterName := helper.EncodeOrganizationAndClusterWorkspace(org, apiBinding.Spec.Reference.Workspace.WorkspaceName)
				key := clusters.ToClusterAwareKey(apiExportClusterName, apiBinding.Spec.Reference.Workspace.ExportName)
				return []string{key}, nil
			}

			return []string{}, nil
		},
	}); err != nil {
		return nil, err
	}

	apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIExport(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIExport(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIExport(obj) },
	})

	return c, nil
}

// TODO godoc
type controller struct {
	queue workqueue.RateLimitingInterface

	crdClusterClient apiextensionclientset.ClusterInterface
	kcpClusterClient kcpclient.ClusterInterface

	//workspaceLister    tenancylister.ClusterWorkspaceLister
	apiBindingsLister  apislisters.APIBindingLister
	apiBindingsIndexer cache.Indexer
	apiExportsLister   apislisters.APIExportLister
}

func (c *controller) enqueue(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	klog.Infof("Queueing APIBinding %q", key)
	c.queue.Add(key)
}

func (c *controller) enqueueAPIExport(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	klog.Infof("Mapping APIExport %q", key)
	list, err := c.apiBindingsIndexer.ByIndex(byWorkspaceExport, key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, apiBinding := range list {
		c.enqueue(apiBinding)
	}
}

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

	klog.Infof("processing key %q", key)

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
	namespace, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
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

	if err := c.reconcile(ctx, obj); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(old.Status, obj.Status) {
		oldData, err := json.Marshal(apisv1alpha1.APIBinding{
			Status: old.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for apibinding %s|%s/%s: %w", clusterName, namespace, name, err)
		}

		newData, err := json.Marshal(apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				UID:             old.UID,
				ResourceVersion: old.ResourceVersion,
			}, // to ensure they appear in the patch as preconditions
			Status: obj.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for apibinding %s|%s/%s: %w", clusterName, namespace, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for apibinding %s|%s/%s: %w", clusterName, namespace, name, err)
		}
		_, uerr := c.kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIBindings().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		return uerr
	}

	return nil
}
