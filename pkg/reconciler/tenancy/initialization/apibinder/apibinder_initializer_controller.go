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
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancylister "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	initializationv1alpha1 "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/apis/initialization/v1alpha1"
	initializationinformer "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/client/informers/externalversions/initialization/v1alpha1"
	initializationlister "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/client/listers/initialization/v1alpha1"
)

const (
	apiBinderControllerName = "kcp-apibinder-initializer-controller"
)

// NewAPIBinder returns a new controller which instantiates APIBindings and waits for them to be fully bound
// in new ClusterWorkspaces of any derivative type from APIBinder.
func NewAPIBinder(
	initializer tenancyv1alpha1.ClusterWorkspaceInitializer,
	kcpClusterClient kcpclient.ClusterInterface,
	clusterWorkspaceInformer tenancyinformer.ClusterWorkspaceInformer,
	apiSetInformer initializationinformer.APISetInformer,
) (*APIBinder, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), apiBinderControllerName)

	c := &APIBinder{
		initializer:      initializer,
		kcpClusterClient: kcpClusterClient,

		clusterWorkspaceLister:  clusterWorkspaceInformer.Lister(),
		clusterWorkspaceIndexer: clusterWorkspaceInformer.Informer().GetIndexer(),

		apiSetLister:  apiSetInformer.Lister(),
		apiSetIndexer: apiSetInformer.Informer().GetIndexer(),

		queue: queue,
	}

	// we don't watch for any events on APISets, as initialization is a one-shot occurrence
	// and updates to how a workspace must be initialized can't be retroactively applied

	clusterWorkspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueClusterWorkspace(obj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueClusterWorkspace(obj)
		},
	})

	return c, nil
}

// APIBinder is a controller which instantiates APIBindings and waits for them to be fully bound
// in new ClusterWorkspaces of any derivative type from APIBinder.
type APIBinder struct {
	initializer tenancyv1alpha1.ClusterWorkspaceInitializer

	kcpClusterClient kcpclient.ClusterInterface

	clusterWorkspaceLister  tenancylister.ClusterWorkspaceLister
	clusterWorkspaceIndexer cache.Indexer

	apiSetLister  initializationlister.APISetLister
	apiSetIndexer cache.Indexer

	queue workqueue.RateLimitingInterface
}

func (b *APIBinder) enqueueClusterWorkspace(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	klog.V(2).Infof("Queueing ClusterWorkspace %q", key)
	b.queue.Add(key)
}

func (b *APIBinder) startWorker(ctx context.Context) {
	for b.processNextWorkItem(ctx) {
	}
}

func (b *APIBinder) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer b.queue.ShutDown()

	klog.Infof("Starting %s controller", apiBinderControllerName)
	defer klog.Infof("Shutting down %s controller", apiBinderControllerName)

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, b.startWorker, time.Second)
	}
	<-ctx.Done()
}

func (b *APIBinder) ShutDown() {
	b.queue.ShutDown()
}

func (b *APIBinder) processNextWorkItem(ctx context.Context) bool {
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
		runtime.HandleError(fmt.Errorf("%s: failed to sync %q, err: %w", apiBinderControllerName, key, err))
		b.queue.AddRateLimited(key)
		return true
	}

	b.queue.Forget(key)
	return true
}

func (b *APIBinder) process(ctx context.Context, key string) error {
	clusterWorkspace, err := b.clusterWorkspaceLister.Get(key)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			parentClusterName, clusterWorkspaceName := clusters.SplitClusterAwareKey(key)
			klog.Errorf("failed to get ClusterWorkspace %s|%s from lister: %v", parentClusterName, clusterWorkspaceName, err)
		}
		return nil // nothing we can do here
	}

	// configuration for this initializer is co-located with the workspace's type
	cwtCluster, cwtName, err := initialization.TypeFrom(initialization.InitializerForReference(clusterWorkspace.Spec.Type))
	if err != nil {
		klog.ErrorS(err, "failed to determine ClusterWorkspaceType name from reference", "reference", clusterWorkspace.Spec.Type, "key", key)
		return nil // nothing we can do here
	}

	configuration, err := b.apiSetLister.Get(clusters.ToClusterAwareKey(cwtCluster, cwtName))
	if err != nil {
		if !apierrors.IsNotFound(err) {
			klog.Errorf("failed to get APISet %s|%s from lister: %v", cwtCluster, cwtName, err)
			return nil // nothing we can do here
		}
		// when no configuration exists, there's nothing to do, but we still need to remove our initializer from the list
		configuration = &initializationv1alpha1.APISet{Spec: initializationv1alpha1.APISetSpec{Bindings: []apisv1alpha1.APIBindingSpec{}}}
	}

	updatedClusterWorkspace := clusterWorkspace.DeepCopy()
	b.reconcile(ctx, updatedClusterWorkspace, configuration)

	// If the object being reconciled changed as a result, update it.
	return b.patchIfNeeded(ctx, clusterWorkspace, updatedClusterWorkspace)
}

func (b *APIBinder) patchIfNeeded(ctx context.Context, old, obj *tenancyv1alpha1.ClusterWorkspace) error {
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

	oldForPatch := tenancyv1alpha1.ClusterWorkspace{
		Status: old.Status,
	}

	oldData, err := json.Marshal(oldForPatch)
	if err != nil {
		return fmt.Errorf("failed to Marshal old data for ClusterWorkspace %s|%s: %w", clusterName, name, err)
	}

	newForPatch := tenancyv1alpha1.ClusterWorkspace{
		Status: obj.Status,
	}
	// to ensure they appear in the patch as preconditions
	newForPatch.UID = old.UID
	newForPatch.ResourceVersion = old.ResourceVersion

	newData, err := json.Marshal(newForPatch)
	if err != nil {
		return fmt.Errorf("failed to Marshal new data for ClusterWorkspace %s|%s: %w", clusterName, name, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return fmt.Errorf("failed to create patch for ClusterWorkspace %s|%s: %w", clusterName, name, err)
	}

	_, err = b.kcpClusterClient.Cluster(clusterName).TenancyV1alpha1().ClusterWorkspaces().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	return err
}
