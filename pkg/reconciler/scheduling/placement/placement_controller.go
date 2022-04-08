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

package placement

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	kubernetesclient "k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	schedulinginformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/scheduling/v1alpha1"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	workloadinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	schedulinglisters "github.com/kcp-dev/kcp/pkg/client/listers/scheduling/v1alpha1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	workloadlisters "github.com/kcp-dev/kcp/pkg/client/listers/workload/v1alpha1"
	locationdomainreconciler "github.com/kcp-dev/kcp/pkg/reconciler/scheduling/locationdomain"
)

const (
	controllerName                  = "kcp-scheduling-placement"
	locationDomainByAssignmentLabel = "locationDomainByAssignmentLabel"
	workloadClustersByWorkspace     = controllerName + "-workloadClustersByWorkspace" // TODO(sttts): only one of these indices is needed
	namespaceByWorkspace            = "namespaceByWorkspace"
	unscheduledNamedspacesKey       = "toSchedulerNamespaces"
)

// NewController returns a new controller placing namespaces onto locations by create
// a placement annotation..
func NewController(
	kubeClusterClient kubernetesclient.ClusterInterface,
	kcpClusterClient kcpclient.ClusterInterface,
	locationDomainInformer schedulinginformers.LocationDomainInformer,
	namespaceClusterInformer coreinformers.NamespaceInformer,
	workloadClusterInformer workloadinformers.WorkloadClusterInformer,
	clusterWorkspaceInformer tenancyinformers.ClusterWorkspaceInformer,
) (*controller, error) {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)

	c := &controller{
		queue: queue,
		enqueueAfter: func(clusterName logicalcluster.LogicalCluster, ns *corev1.Namespace, duration time.Duration) {
			key := clusters.ToClusterAwareKey(clusterName, ns.Name)
			queue.AddAfter(key, duration)
		},

		kubeClusterClient: kubeClusterClient,
		kcpClusterClient:  kcpClusterClient,

		locationDomainLister:  locationDomainInformer.Lister(),
		locationDomainIndexer: locationDomainInformer.Informer().GetIndexer(),

		namespaceLister:  namespaceClusterInformer.Lister(),
		namespaceIndexer: namespaceClusterInformer.Informer().GetIndexer(),

		workloadClusterLister:  workloadClusterInformer.Lister(),
		workloadClusterIndexer: workloadClusterInformer.Informer().GetIndexer(),

		clusterWorkspaceLister:  clusterWorkspaceInformer.Lister(),
		clusterWorkspaceIndexer: clusterWorkspaceInformer.Informer().GetIndexer(),
	}

	if err := locationDomainInformer.Informer().AddIndexers(cache.Indexers{
		locationDomainByAssignmentLabel: indexLocationDomainByAssignmentLabel,
	}); err != nil {
		return nil, err
	}

	if err := workloadClusterInformer.Informer().AddIndexers(cache.Indexers{
		workloadClustersByWorkspace: indexWorkloadClustersByWorkspace,
	}); err != nil {
		return nil, err
	}

	if err := namespaceClusterInformer.Informer().AddIndexers(cache.Indexers{
		namespaceByWorkspace:      indexNamespacByWorkspace,
		unscheduledNamedspacesKey: indexUnscheduledNamespaces,
	}); err != nil {
		return nil, err
	}

	clusterWorkspaceInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch cluster := obj.(type) {
			case *tenancyv1alpha1.ClusterWorkspace:
				key := schedulingv1alpha1.LocationDomainAssignmentLabelKeyForType(locationdomainreconciler.LocationDomainTypeWorkload)
				value, found := cluster.Labels[key]
				return found && value != ""
			case cache.DeletedFinalStateUnknown:
				return true
			default:
				return false
			}
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueueClusterWorkspace(obj) },
			UpdateFunc: func(_, obj interface{}) { c.enqueueClusterWorkspace(obj) },
			DeleteFunc: func(obj interface{}) { c.enqueueClusterWorkspace(obj) },
		},
	})

	namespaceClusterInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch ns := obj.(type) {
			case *corev1.Namespace:
				_, found := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]
				return !found // only without annotation, neither empty (= don't touch me) nor non-empty (= already scheduled)
			case cache.DeletedFinalStateUnknown:
				return true
			default:
				return false
			}
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueueNamespace(obj) },
			UpdateFunc: func(_, obj interface{}) { c.enqueueNamespace(obj) },
			DeleteFunc: func(obj interface{}) { c.enqueueNamespace(obj) },
		},
	})

	workloadClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) { c.enqueueWorkloadCluster(obj) },
		UpdateFunc: func(old, obj interface{}) {
			oldCluster, ok := old.(*workloadv1alpha1.WorkloadCluster)
			if !ok {
				return
			}
			objCluster, ok := obj.(*workloadv1alpha1.WorkloadCluster)
			if !ok {
				return
			}

			// only enqueue if spec or conditions change.
			oldCluster = oldCluster.DeepCopy()
			oldCluster.Status.LastSyncerHeartbeatTime = objCluster.Status.LastSyncerHeartbeatTime

			if !equality.Semantic.DeepEqual(oldCluster, objCluster) {
				c.enqueueWorkloadCluster(obj)
			}
		},
		DeleteFunc: func(obj interface{}) { c.enqueueWorkloadCluster(obj) },
	})

	return c, nil
}

// controller
type controller struct {
	queue        workqueue.RateLimitingInterface
	enqueueAfter func(logicalcluster.LogicalCluster, *corev1.Namespace, time.Duration)

	kubeClusterClient kubernetesclient.ClusterInterface
	kcpClusterClient  kcpclient.ClusterInterface

	locationDomainLister  schedulinglisters.LocationDomainLister
	locationDomainIndexer cache.Indexer

	// TODO(sttts): this shouldn't be needed if we had the domain assignment inside of the workspace (we should).
	//              Having a replica of the ClusterWorkspace like "." in filesystem would give us that.
	clusterWorkspaceLister  tenancylisters.ClusterWorkspaceLister
	clusterWorkspaceIndexer cache.Indexer

	namespaceLister  corelisters.NamespaceLister
	namespaceIndexer cache.Indexer

	workloadClusterLister  workloadlisters.WorkloadClusterLister
	workloadClusterIndexer cache.Indexer
}

// enqueueLocationDomain enqueues all namespaces.
func (c *controller) enqueueClusterWorkspace(obj interface{}) {
	cluster, ok := obj.(*tenancyv1alpha1.ClusterWorkspace)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a ClusterWorkspace, but is %T", obj))
		return
	}

	fullLogicalCluster := logicalcluster.From(cluster).Join(cluster.Name)
	namespaces, err := c.namespaceIndexer.ByIndex(namespaceByWorkspace, fullLogicalCluster.String())
	if err != nil {
		runtime.HandleError(fmt.Errorf("error getting namespaces for cluster %s: %w", fullLogicalCluster.String(), err))
		return
	}

	for _, obj := range namespaces {
		ns, ok := obj.(*corev1.Namespace)
		if !ok {
			runtime.HandleError(fmt.Errorf("obj is supposed to be a Namespace, but is %T", obj))
			continue
		}

		klog.Infof("Mapping ClusterWorkspace %s|%s to namespace %s", logicalcluster.From(cluster).String(), cluster.Name, ns.Name)
		key := clusters.ToClusterAwareKey(logicalcluster.From(ns), ns.Name)
		c.queue.Add(key)
	}
}

// enqueueNamespace enqueues a namespace.
func (c *controller) enqueueNamespace(obj interface{}) {
	ns, ok := obj.(*corev1.Namespace)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a Location, but is %T", obj))
		return
	}

	key := clusters.ToClusterAwareKey(logicalcluster.From(ns), ns.Name)
	klog.Infof("Queueing Namespace %s|%s", logicalcluster.From(ns).String(), ns.Name, key)
	c.queue.Add(key)
}

func (c *controller) enqueueWorkloadCluster(obj interface{}) {
	ns, ok := obj.(*workloadv1alpha1.WorkloadCluster)
	if !ok {
		runtime.HandleError(fmt.Errorf("obj is supposed to be a Location, but is %T", obj))
		return
	}

	// TODO(sttts): we might want to find the LocationDomains matching this workload cluster, and then requeue
	//              the unscheduled namespaces of workspaces that are assigned to those domains, at least when
	//              the workload cluster either increase allocable resources, is new or became ready.
	//              For now we requeue unscheduled namespaces. But that won't scale.
	klog.Infof("Not queueing any namespace yet due to WorkloadCluster %s|%s update", logicalcluster.From(ns).String(), ns.Name)
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

	obj, err := c.namespaceLister.Get(key) // TODO: clients need a way to scope down the lister per-cluster
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
		oldData, err := json.Marshal(corev1.Namespace{
			Status: old.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal old data for LocationDomain %s|%s/%s: %w", clusterName, namespace, name, err)
		}

		newData, err := json.Marshal(corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				UID:             old.UID,
				ResourceVersion: old.ResourceVersion,
			}, // to ensure they appear in the patch as preconditions
			Status: obj.Status,
		})
		if err != nil {
			return fmt.Errorf("failed to Marshal new data for LocationDomain %s|%s/%s: %w", clusterName, namespace, name, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return fmt.Errorf("failed to create patch for LocationDomain %s|%s/%s: %w", clusterName, namespace, name, err)
		}
		_, uerr := c.kcpClusterClient.Cluster(clusterName).SchedulingV1alpha1().LocationDomains().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		return uerr
	}

	return nil
}
