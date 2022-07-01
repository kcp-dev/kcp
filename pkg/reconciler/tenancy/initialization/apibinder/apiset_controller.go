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

package apibinder

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	apisinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	initializationv1alpha1 "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/apis/initialization/v1alpha1"
	initializationclient "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/client/clientset/versioned"
	initializationinformer "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/client/informers/externalversions/initialization/v1alpha1"
	initializationlister "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/client/listers/initialization/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
)

const (
	apiSetControllerName = "kcp-apiset-controller"
	byExport             = apiSetControllerName + "-byExport"
)

type CreateAPIDefinitionFunc func(apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, identityHash string) (apidefinition.APIDefinition, error)

// NewAPISetValidator returns a new controller which watches APISets ensures they are valid.
func NewAPISetValidator(
	initializationClusterClient initializationclient.ClusterInterface,
	apiSetInformer initializationinformer.APISetInformer,
	apiExportInformer apisinformer.APIExportInformer,
) (*APISetValidator, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), apiSetControllerName)

	c := &APISetValidator{
		initializationClusterClient: initializationClusterClient,

		apiSetLister:  apiSetInformer.Lister(),
		apiSetIndexer: apiSetInformer.Informer().GetIndexer(),

		apiExportLister:  apiExportInformer.Lister(),
		apiExportIndexer: apiExportInformer.Informer().GetIndexer(),

		queue: queue,
	}

	apiExportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIExport(obj)
		},
	})

	apiSetInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueAPISet(obj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPISet(obj)
		},
	})

	if err := apiSetInformer.Informer().AddIndexers(cache.Indexers{
		byExport: indexByExport,
	}); err != nil {
		return nil, err
	}

	return c, nil
}

// APISetValidator is a controller which watches APISets ensures they are valid.
type APISetValidator struct {
	initializationClusterClient initializationclient.ClusterInterface

	apiSetLister  initializationlister.APISetLister
	apiSetIndexer cache.Indexer

	apiExportLister  apislisters.APIExportLister
	apiExportIndexer cache.Indexer

	queue workqueue.RateLimitingInterface
}

func (b *APISetValidator) enqueueAPISet(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	b.queue.Add(key)
}

func (c *APISetValidator) enqueueAPIExport(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	clusterName, name := clusters.SplitClusterAwareKey(key)

	apiSets, err := c.apiSetIndexer.ByIndex(byExport, key)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	for _, obj := range apiSets {
		apiSet := obj.(*initializationv1alpha1.APISet)
		klog.V(2).Infof("Queueing APISet %s|%s for APIExport %s|%s", logicalcluster.From(apiSet), apiSet.Name, clusterName, name)
		c.enqueueAPIExport(obj)
	}
}

func (b *APISetValidator) startWorker(ctx context.Context) {
	for b.processNextWorkItem(ctx) {
	}
}

func (b *APISetValidator) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer b.queue.ShutDown()

	klog.Infof("Starting %s controller", apiSetControllerName)
	defer klog.Infof("Shutting down %s controller", apiSetControllerName)

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, b.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (b *APISetValidator) ShutDown() {
	b.queue.ShutDown()
}

func (b *APISetValidator) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := b.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer b.queue.Done(key)

	if err := b.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%s: failed to sync %q, err: %w", apiSetControllerName, key, err))
		b.queue.AddRateLimited(key)
		return true
	}

	b.queue.Forget(key)
	return true
}

func (b *APISetValidator) process(ctx context.Context, key string) error {
	apiSet, err := b.apiSetLister.Get(key)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			clusterName, name := clusters.SplitClusterAwareKey(key)
			klog.Errorf("failed to get APISet %s|%s from lister: %v", clusterName, name, err)
		}
		return nil // nothing we can do here
	}

	updatedAPISet := apiSet.DeepCopy()
	b.reconcile(ctx, updatedAPISet)

	// If the object being reconciled changed as a result, update it.
	return b.patchIfNeeded(ctx, apiSet, updatedAPISet)
}

func (b *APISetValidator) patchIfNeeded(ctx context.Context, old, obj *initializationv1alpha1.APISet) error {
	specOrObjectMetaChanged := !equality.Semantic.DeepEqual(old.Spec, obj.Spec) || !equality.Semantic.DeepEqual(old.ObjectMeta, obj.ObjectMeta)
	if specOrObjectMetaChanged {
		klog.Fatalf("Programmer error: this controller must not touch spec")
	}

	statusChanged := !equality.Semantic.DeepEqual(old.Status, obj.Status)
	if !statusChanged {
		return nil
	}

	clusterName := logicalcluster.From(old)
	name := old.Name

	oldForPatch := initializationv1alpha1.APISet{
		Status: old.Status,
	}

	oldData, err := json.Marshal(oldForPatch)
	if err != nil {
		return fmt.Errorf("failed to Marshal old data for APISet %s|%s: %w", clusterName, name, err)
	}

	newForPatch := initializationv1alpha1.APISet{
		Status: obj.Status,
	}
	// to ensure they appear in the patch as preconditions
	newForPatch.UID = old.UID
	newForPatch.ResourceVersion = old.ResourceVersion

	newData, err := json.Marshal(newForPatch)
	if err != nil {
		return fmt.Errorf("failed to Marshal new data for APISet %s|%s: %w", clusterName, name, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return fmt.Errorf("failed to create patch for APISet %s|%s: %w", clusterName, name, err)
	}

	_, err = b.initializationClusterClient.Cluster(clusterName).InitializationV1alpha1().APISets().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	return err
}
