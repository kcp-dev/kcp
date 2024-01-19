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

package logicalclusterdeletion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	kcpmetadata "github.com/kcp-dev/client-go/metadata"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	"github.com/kcp-dev/kcp/pkg/reconciler/core/logicalclusterdeletion/deletion"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	corev1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/core/v1alpha1"
	apisv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"
)

const (
	ControllerName = "kcp-logicalcluster-deletion"
)

var (
	background        = metav1.DeletePropagationBackground
	backgroudDeletion = metav1.DeleteOptions{PropagationPolicy: &background}
)

func NewController(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	kcpClusterClient kcpclientset.ClusterInterface,
	logicalClusterAdminConfig *rest.Config,
	externalLogicalClusterAdminConfig *rest.Config,
	metadataClusterClient kcpmetadata.ClusterInterface,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	discoverResourcesFn func(clusterName logicalcluster.Path) ([]*metav1.APIResourceList, error),
	apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
) *Controller {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	isBoundResource := func(clusterName logicalcluster.Name, group, resource string) (bool, error) {
		apiBindings, err := apiBindingInformer.Cluster(clusterName).Lister().List(labels.Everything())
		if err != nil {
			return false, err
		}
		for _, apiBinding := range apiBindings {
			for _, boundResource := range apiBinding.Status.BoundResources {
				if boundResource.Group == group && boundResource.Resource == resource {
					return true, nil
				}
			}
		}
		return false, nil
	}

	c := &Controller{
		queue:                             queue,
		kubeClusterClient:                 kubeClusterClient,
		kcpClusterClient:                  kcpClusterClient,
		logicalClusterAdminConfig:         logicalClusterAdminConfig,
		externalLogicalClusterAdminConfig: externalLogicalClusterAdminConfig,
		metadataClusterClient:             metadataClusterClient,
		logicalClusterLister:              logicalClusterInformer.Lister(),
		deleter:                           deletion.NewWorkspacedResourcesDeleter(metadataClusterClient, discoverResourcesFn, isBoundResource),
		commit:                            committer.NewCommitter[*LogicalCluster, Patcher, *LogicalClusterSpec, *LogicalClusterStatus](kcpClusterClient.CoreV1alpha1().LogicalClusters()),
	}

	_, _ = logicalClusterInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch obj := obj.(type) {
			case *corev1alpha1.LogicalCluster:
				return !obj.DeletionTimestamp.IsZero()
			default:
				return false
			}
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    func(obj interface{}) { c.enqueue(obj) },
			UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		},
	})

	return c
}

type LogicalCluster = corev1alpha1.LogicalCluster
type LogicalClusterSpec = corev1alpha1.LogicalClusterSpec
type LogicalClusterStatus = corev1alpha1.LogicalClusterStatus
type Patcher = corev1alpha1client.LogicalClusterInterface
type Resource = committer.Resource[*LogicalClusterSpec, *LogicalClusterStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

type Controller struct {
	queue workqueue.RateLimitingInterface

	kubeClusterClient kcpkubernetesclientset.ClusterInterface
	kcpClusterClient  kcpclientset.ClusterInterface

	logicalClusterAdminConfig         *rest.Config
	externalLogicalClusterAdminConfig *rest.Config
	dynamicFrontProxyClient           kcpdynamic.ClusterInterface

	metadataClusterClient kcpmetadata.ClusterInterface

	logicalClusterLister corev1alpha1listers.LogicalClusterClusterLister

	deleter deletion.WorkspaceResourcesDeleterInterface

	commit CommitFunc
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing LogicalCluster")
	c.queue.Add(key)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	// a client needed to remove the finalizer from the logical cluster on a different shard
	frontProxyConfig := rest.CopyConfig(c.externalLogicalClusterAdminConfig)
	frontProxyConfig = rest.AddUserAgent(frontProxyConfig, ControllerName)
	dynamicFrontProxyClient, err := kcpdynamic.NewForConfig(frontProxyConfig)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	c.dynamicFrontProxyClient = dynamicFrontProxyClient

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startWorker(ctx) }, time.Second, ctx.Done())
	}

	<-ctx.Done()
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
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	startTime := time.Now()
	err := c.process(ctx, key)

	if err == nil {
		// no error, forget this entry and return
		c.queue.Forget(key)
		return true
	}

	var estimate *deletion.ResourcesRemainingError
	if errors.As(err, &estimate) {
		t := estimate.Estimate/2 + 1
		duration := time.Duration(t) * time.Second
		logger.V(2).Error(err, "content remaining in logical cluster after a wait, waiting more to continue", "duration", time.Since(startTime), "waiting", duration)

		c.queue.AddAfter(key, duration)
	} else {
		// rather than wait for a full resync, re-add the logical cluster to the queue to be processed
		c.queue.AddRateLimited(key)
		runtime.HandleError(fmt.Errorf("deletion of logical cluster %v failed: %w", key, err))
	}

	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}
	logicalCluster, deleteErr := c.logicalClusterLister.Cluster(clusterName).Get(name)
	if apierrors.IsNotFound(deleteErr) {
		logger.V(3).Info("Workspace has been deleted")
		return nil
	}
	if deleteErr != nil {
		runtime.HandleError(fmt.Errorf("unable to retrieve logical cluster %v from store: %w", key, deleteErr))
		return deleteErr
	}

	logger = logging.WithObject(logger, logicalCluster)
	ctx = klog.NewContext(ctx, logger)

	if logicalCluster.DeletionTimestamp.IsZero() {
		return nil
	}

	logicalClusterCopy := logicalCluster.DeepCopy()

	logger.V(2).Info("deleting logical cluster")
	startTime := time.Now()
	deleteErr = c.deleter.Delete(ctx, logicalClusterCopy)
	if deleteErr == nil {
		logger.V(4).Info("finished deleting logical cluster content", "duration", time.Since(startTime))
		return c.finalizeWorkspace(ctx, logicalClusterCopy)
	}

	errs := []error{deleteErr}

	oldResource := &Resource{ObjectMeta: logicalCluster.ObjectMeta, Spec: &logicalCluster.Spec, Status: &logicalCluster.Status}
	newResource := &Resource{ObjectMeta: logicalClusterCopy.ObjectMeta, Spec: &logicalClusterCopy.Spec, Status: &logicalClusterCopy.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}

// finalizeNamespace removes the specified finalizer and finalizes the logical cluster.
func (c *Controller) finalizeWorkspace(ctx context.Context, ws *corev1alpha1.LogicalCluster) error {
	logger := klog.FromContext(ctx)
	for i := range ws.Finalizers {
		if ws.Finalizers[i] == deletion.LogicalClusterDeletionFinalizer {
			ws.Finalizers = append(ws.Finalizers[:i], ws.Finalizers[i+1:]...)
			clusterName := logicalcluster.From(ws)

			// TODO(hasheddan): ClusterRole and ClusterRoleBinding cleanup
			// should be handled by garbage collection when the controller is
			// implemented.
			logger.Info("deleting cluster roles")
			if err := c.kubeClusterClient.Cluster(clusterName.Path()).RbacV1().ClusterRoles().DeleteCollection(ctx, backgroudDeletion, metav1.ListOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("could not delete clusterroles for logical cluster %s: %w", clusterName, err)
			}
			logger.Info("deleting cluster role bindings")
			if err := c.kubeClusterClient.Cluster(clusterName.Path()).RbacV1().ClusterRoleBindings().DeleteCollection(ctx, backgroudDeletion, metav1.ListOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("could not delete clusterrolebindings for logical cluster %s: %w", clusterName, err)
			}

			if ws.Spec.Owner != nil {
				gvr := schema.GroupVersionResource{
					Resource: ws.Spec.Owner.Resource,
				}
				comps := strings.SplitN(ws.Spec.Owner.APIVersion, "/", 2)
				if len(comps) == 2 {
					gvr.Group = comps[0]
					gvr.Version = comps[1]
				} else {
					gvr.Version = comps[0]
				}
				uid := ws.Spec.Owner.UID
				logger = logger.WithValues("owner.gvr", gvr, "owner.uid", uid, "owner.name", ws.Spec.Owner.Name, "owner.namespace", ws.Spec.Owner.Namespace, "owner.cluster", ws.Spec.Owner.Cluster)

				// remove finalizer from owner
				logger.Info("checking owner for finalizer")
				clusterPath := logicalcluster.NewPath(ws.Spec.Owner.Cluster)
				obj, err := c.dynamicFrontProxyClient.Cluster(clusterPath).Resource(gvr).Namespace(ws.Spec.Owner.Namespace).Get(ctx, ws.Spec.Owner.Name, metav1.GetOptions{})
				if err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("could not get owner %s %s/%s in cluster %s: %w", gvr, ws.Spec.Owner.Namespace, ws.Spec.Owner.Name, ws.Spec.Owner.Cluster, err)
				} else if err == nil && obj.GetUID() != uid {
					logger.Info("owner has changed, skipping finalizer removal")
					return fmt.Errorf("could not get owner %s %s/%s in cluster %s is of wrong UID: %w", gvr, ws.Spec.Owner.Namespace, ws.Spec.Owner.Name, ws.Spec.Owner.Cluster, err)
				} else if err == nil {
					finalizers := sets.New[string](obj.GetFinalizers()...)
					if finalizers.Has(corev1alpha1.LogicalClusterFinalizer) {
						logger.Info("removing finalizer from owner")
						finalizers.Delete(corev1alpha1.LogicalClusterFinalizer)
						obj.SetFinalizers(sets.List[string](finalizers))
						if obj, err = c.dynamicFrontProxyClient.Cluster(clusterPath).Resource(gvr).Namespace(ws.Spec.Owner.Namespace).Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
							return fmt.Errorf("could not remove finalizer from owner %s %s/%s in cluster %s: %w", gvr, ws.Spec.Owner.Namespace, ws.Spec.Owner.Name, ws.Spec.Owner.Cluster, err)
						}
					}

					// delete owner
					if obj.GetDeletionTimestamp().IsZero() && ws.Spec.DirectlyDeletable {
						logger.Info("deleting owner")
						if err := c.dynamicFrontProxyClient.Cluster(clusterPath).Resource(gvr).Namespace(ws.Spec.Owner.Namespace).Delete(ctx, ws.Spec.Owner.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil && !apierrors.IsNotFound(err) {
							return fmt.Errorf("could not delete owner %s %s/%s in cluster %s: %w", gvr, ws.Spec.Owner.Namespace, ws.Spec.Owner.Name, ws.Spec.Owner.Cluster, err)
						}
					}
				}
			}

			logger.V(2).Info("removing finalizer from LogicalCluster")
			_, err := c.kcpClusterClient.CoreV1alpha1().LogicalClusters().Cluster(clusterName.Path()).Update(ctx, ws, metav1.UpdateOptions{})
			return err
		}
	}

	return nil
}
