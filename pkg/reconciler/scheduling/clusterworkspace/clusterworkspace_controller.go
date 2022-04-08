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

package clusterworkspace

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

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

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	schedulinginformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/scheduling/v1alpha1"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	schedulinglisters "github.com/kcp-dev/kcp/pkg/client/listers/scheduling/v1alpha1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/namespace"
)

const (
	controllerName              = "kcp-scheduling-workspace"
	locationDomainByWorkspace   = "locationDomainByWorkspace"
	workspacesByWorkspacePrefix = "workspacesByWorkspacePrefix"
)

// NewController returns a new controller assigning workspaces to LocationDomains.
func NewController(
	kcpClusterClient kcpclient.ClusterInterface,
	locationDomainInformer schedulinginformers.LocationDomainInformer,
	clusterWorkloadInformer tenancyinformers.ClusterWorkspaceInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue: queue,
		enqueueAfter: func(clusterName logicalcluster.LogicalCluster, cluster *tenancyv1alpha1.ClusterWorkspace, duration time.Duration) {
			key := clusters.ToClusterAwareKey(clusterName, cluster.Name)
			queue.AddAfter(key, duration)
		},
		kcpClusterClient:        kcpClusterClient,
		locationDomainLister:    locationDomainInformer.Lister(),
		locationDomainIndexer:   locationDomainInformer.Informer().GetIndexer(),
		clusterWorkspaceLister:  clusterWorkloadInformer.Lister(),
		clusterWorkspaceIndexer: clusterWorkloadInformer.Informer().GetIndexer(),
	}

	if err := locationDomainInformer.Informer().AddIndexers(cache.Indexers{
		locationDomainByWorkspace: indexLocationDomainByWorkspace,
	}); err != nil {
		return nil, err
	}

	if err := clusterWorkloadInformer.Informer().AddIndexers(cache.Indexers{
		workspacesByWorkspacePrefix: indexWorkspacesByWorkspacePrefix,
	}); err != nil {
		return nil, err
	}

	clusterWorkloadInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			if obj, ok := obj.(*tenancyv1alpha1.ClusterWorkspace); ok {
				// TODO(sttts): find more generic way to disable scheduling
				return obj.Labels[namespace.WorkspaceSchedulableLabel] == "true"
			}
			return false
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) { c.enqueueClusterWorkspace(obj) },
			UpdateFunc: func(old, obj interface{}) {
				oldCluster, ok := old.(*tenancyv1alpha1.ClusterWorkspace)
				if !ok {
					return
				}
				objCluster, ok := obj.(*tenancyv1alpha1.ClusterWorkspace)
				if !ok {
					return
				}

				if !equality.Semantic.DeepEqual(oldCluster.Labels, objCluster.Labels) {
					c.enqueueClusterWorkspace(obj)
				}
			},
			DeleteFunc: func(obj interface{}) { c.enqueueClusterWorkspace(obj) },
		},
	})

	locationDomainInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueLocationDomain(obj) },
		UpdateFunc: func(old, obj interface{}) { c.enqueueLocationDomain(obj) },
		DeleteFunc: func(obj interface{}) { c.enqueueLocationDomain(obj) },
	})

	return c, nil
}

type controller struct {
	queue        workqueue.RateLimitingInterface
	enqueueAfter func(logicalcluster.LogicalCluster, *tenancyv1alpha1.ClusterWorkspace, time.Duration)

	kcpClusterClient kcpclient.ClusterInterface

	locationDomainLister    schedulinglisters.LocationDomainLister
	locationDomainIndexer   cache.Indexer
	clusterWorkspaceLister  tenancylisters.ClusterWorkspaceLister
	clusterWorkspaceIndexer cache.Indexer
}

// enqueueClusterWorkspace enqueues an ClusterWorkspaces.
func (c *controller) enqueueClusterWorkspace(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	klog.Infof("Queueing ClusterWorkspace %q", key)
	c.queue.Add(key)
}

// enqueueLocationDomain enqueues the ClusterWorkspaces under the LocationDomain's workspace.
func (c *controller) enqueueLocationDomain(obj interface{}) {
	domain, ok := obj.(*schedulingv1alpha1.LocationDomain)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a LocationDomain, but is %T", obj))
		return
	}

	clusterName := logicalcluster.From(domain)
	clusterWorkspaces, err := c.clusterWorkspaceIndexer.ByIndex(workspacesByWorkspacePrefix, clusterName.String())
	if err != nil {
		runtime.HandleError(fmt.Errorf("error getting ClusterWorkspaces under %s: %w", clusterName.String(), err))
		return
	}

	for _, ws := range clusterWorkspaces {
		c.enqueueClusterWorkspace(ws)
	}
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
	namespace, clusterAwareName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.Errorf("invalid key: %q: %v", key, err)
		return nil
	}
	clusterName, name := clusters.SplitClusterAwareKey(clusterAwareName)

	obj, err := c.clusterWorkspaceLister.Get(key) // TODO: clients need a way to scope down the lister per-cluster
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
		oldData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspace{
			Status: old.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for ClusterWorkspace %s|%s/%s: %w", clusterName, namespace, name, err)
		}

		newData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspace{
			ObjectMeta: metav1.ObjectMeta{
				UID:             old.UID,
				ResourceVersion: old.ResourceVersion,
			}, // to ensure they appear in the patch as preconditions
			Status: obj.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for ClusterWorkspace %s|%s/%s: %w", clusterName, namespace, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for ClusterWorkspace %s|%s/%s: %w", clusterName, namespace, name, err)
		}
		_, uerr := c.kcpClusterClient.Cluster(clusterName).TenancyV1alpha1().ClusterWorkspaces().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		return uerr
	}

	return nil
}
