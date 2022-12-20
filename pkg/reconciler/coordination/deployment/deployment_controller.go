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

package deployment

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	v1 "github.com/kcp-dev/client-go/informers/apps/v1"
	kubernetesclient "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	appsv1client "k8s.io/client-go/kubernetes/typed/apps/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/workload/helpers"
	"github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	"github.com/kcp-dev/kcp/tmc/pkg/coordination"
)

const (
	controllerName = "kcp-deployment-coordination"
)

type Deployment = appsv1.Deployment
type DeploymentSpec = appsv1.DeploymentSpec
type DeploymentStatus = appsv1.DeploymentStatus
type Patcher = appsv1client.DeploymentInterface
type Resource = committer.Resource[*DeploymentSpec, *DeploymentStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

// NewController returns a new controller instance.
func NewController(
	ctx context.Context,
	kubeClusterClient kubernetesclient.ClusterInterface,
	deploymentClusterInformer v1.DeploymentClusterInformer,
) (*controller, error) {
	lister := deploymentClusterInformer.Lister()
	informer := deploymentClusterInformer.Informer()

	c := &controller{
		upstreamViewQueue:   workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName+"upstream-view"),
		syncerViewQueue:     workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName+"syncer-view"),
		syncerViewRetriever: coordination.NewDefaultSyncerViewManager[*appsv1.Deployment](),
		gvr:                 appsv1.SchemeGroupVersion.WithResource("deployments"),

		getDeployment: func(clusterName logicalcluster.Name, namespace, name string) (*appsv1.Deployment, error) {
			return lister.Cluster(clusterName).Deployments(namespace).Get(name)
		},
		patcher: func(clusterName logicalcluster.Name, namespace string) committer.Patcher[*appsv1.Deployment] {
			return kubeClusterClient.AppsV1().Deployments().Cluster(clusterName.Path()).Namespace(namespace)
		},
	}

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			enqueue(obj, c.upstreamViewQueue, logger.WithValues("view", "upstream"))
			enqueue(obj, c.syncerViewQueue, logger.WithValues("view", "syncer"))
		},
		UpdateFunc: func(old, new interface{}) {
			oldObj, ok := old.(coordination.Object)
			if !ok {
				utilruntime.HandleError(fmt.Errorf("resource should be a coordination.Object, but was %T", oldObj))
				return
			}
			newObj, ok := new.(coordination.Object)
			if !ok {
				utilruntime.HandleError(fmt.Errorf("resource should be a coordination.Object, but was %T", newObj))
				return
			}

			if coordination.AnySyncerViewChanged(oldObj, newObj) {
				enqueue(new, c.syncerViewQueue, logger.WithValues("view", "syncer"))
			}
			if coordination.UpstreamViewChanged(oldObj, newObj, deploymentContentsEqual) {
				enqueue(new, c.upstreamViewQueue, logger.WithValues("view", "upstream"))
			}
		},
	})

	return c, nil
}

// controller reconciles watches deployments and coordinates them between SyncTargets
type controller struct {
	upstreamViewQueue workqueue.RateLimitingInterface
	syncerViewQueue   workqueue.RateLimitingInterface

	getDeployment func(clusterName logicalcluster.Name, namespace, name string) (*appsv1.Deployment, error)
	patcher       func(clusterName logicalcluster.Name, namespace string) committer.Patcher[*appsv1.Deployment]

	syncerViewRetriever coordination.SyncerViewRetriever[*appsv1.Deployment]
	gvr                 schema.GroupVersionResource
}

func (c *controller) committer(clusterName logicalcluster.Name, namespace string) CommitFunc {
	return committer.NewCommitterScoped[*Deployment, Patcher, *DeploymentSpec, *DeploymentStatus](c.patcher(clusterName, namespace))
}

func filter[K comparable, V interface{}](aMap map[K]V, keep func(key K) bool) map[K]V {
	result := make(map[K]V)
	for key, val := range aMap {
		if keep(key) {
			result[key] = val
		}
	}
	return result
}

func deploymentContentsEqual(old, new interface{}) bool {
	oldDeployment, ok := old.(*appsv1.Deployment)
	if !ok {
		return false
	}
	newDeployment, ok := new.(*appsv1.Deployment)
	if !ok {
		return false
	}

	if !equality.Semantic.DeepEqual(oldDeployment.Labels, newDeployment.Labels) {
		return false
	}

	oldAnnotations := filter(oldDeployment.Annotations, func(key string) bool {
		return !strings.HasPrefix(key, v1alpha1.ClusterSpecDiffAnnotationPrefix)
	})
	newAnnotations := filter(newDeployment.Annotations, func(key string) bool {
		return !strings.HasPrefix(key, v1alpha1.ClusterSpecDiffAnnotationPrefix)
	})
	if !equality.Semantic.DeepEqual(oldAnnotations, newAnnotations) {
		return false
	}

	oldReplicas := oldDeployment.Spec.Replicas
	newReplicas := newDeployment.Spec.Replicas

	if oldReplicas == nil && newReplicas == nil {
		return true
	}

	return oldReplicas != nil && newReplicas != nil && *oldReplicas == *newReplicas
}

// enqueue adds the logical cluster to the queue.
func enqueue(obj interface{}, queue workqueue.RateLimitingInterface, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	logger = logging.WithQueueKey(logger, key)
	logger.V(2).Info("queueing deployment")
	queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.upstreamViewQueue.ShutDown()
	defer c.syncerViewQueue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startUpstreamViewWorker, time.Second)
		go wait.UntilWithContext(ctx, c.startSyncerViewWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *controller) startUpstreamViewWorker(ctx context.Context) {
	logger := klog.FromContext(ctx).WithValues("view", "upstream")
	ctx = klog.NewContext(ctx, logger)
	for processNextWorkItem(ctx, c.upstreamViewQueue, c.processUpstreamView) {
	}
}

func (c *controller) startSyncerViewWorker(ctx context.Context) {
	logger := klog.FromContext(ctx).WithValues("view", "syncer")
	ctx = klog.NewContext(ctx, logger)
	for processNextWorkItem(ctx, c.syncerViewQueue, c.processSyncerView) {
	}
}

func processNextWorkItem(ctx context.Context, queue workqueue.RateLimitingInterface, process func(context.Context, string) error) bool {
	// Wait until there is a new item in the working queue
	k, quit := queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer queue.Done(key)

	if err := process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		queue.AddRateLimited(key)
		return true
	}
	queue.Forget(key)
	return true
}

func (c *controller) processUpstreamView(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	clusterName, namespace, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "failed to split key, dropping")
		return nil
	}

	deployment, err := c.getDeployment(clusterName, namespace, name)
	if err != nil {
		return err
	}
	logger = logging.WithObject(logger, deployment)
	ctx = klog.NewContext(ctx, logger)

	syncIntents, err := helpers.GetSyncIntents(deployment)
	if err != nil {
		return err
	}

	updated := deployment.DeepCopy()

	syncerViews := sets.NewString()
	for syncTarget, syncTargetSyncing := range syncIntents {
		if syncTargetSyncing.ResourceState == v1alpha1.ResourceStateSync && syncTargetSyncing.DeletionTimestamp == nil {
			syncerViews.Insert(syncTarget)
		}
	}

	newSpecDiffAnnotation := make(map[string]string, len(syncerViews))
	if len(syncerViews) > 0 {
		replicasEach := int64(*updated.Spec.Replicas) / int64(syncerViews.Len())
		rest := int64(*updated.Spec.Replicas) % int64(syncerViews.Len())

		for index, syncTargetKey := range syncerViews.List() {
			replicasToSet := replicasEach
			if index == 0 {
				replicasToSet += rest
			}
			newSpecDiffAnnotation[v1alpha1.ClusterSpecDiffAnnotationPrefix+syncTargetKey] = fmt.Sprintf(`[{ "op": "replace", "path": "/replicas", "value": %d }]`, replicasToSet)
		}
	}
	for key := range updated.Annotations {
		if _, found := newSpecDiffAnnotation[key]; !found &&
			strings.HasPrefix(key, v1alpha1.ClusterSpecDiffAnnotationPrefix) {
			delete(updated.Annotations, key)
		}
	}

	if updated.Annotations == nil {
		updated.Annotations = make(map[string]string, len(newSpecDiffAnnotation))
	}
	for key, value := range newSpecDiffAnnotation {
		updated.Annotations[key] = value
	}

	return c.committer(clusterName, namespace)(ctx,
		&committer.Resource[*appsv1.DeploymentSpec, *appsv1.DeploymentStatus]{
			ObjectMeta: deployment.ObjectMeta,
		},
		&committer.Resource[*appsv1.DeploymentSpec, *appsv1.DeploymentStatus]{
			ObjectMeta: updated.ObjectMeta,
		})
}

func (c *controller) processSyncerView(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	clusterName, namespace, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "failed to split key, dropping")
		return nil
	}

	deployment, err := c.getDeployment(clusterName, namespace, name)
	if err != nil {
		return err
	}
	logger = logging.WithObject(logger, deployment)
	ctx = klog.NewContext(ctx, logger)

	summarizedStatus := appsv1.DeploymentStatus{}

	syncerViews, err := c.syncerViewRetriever.GetAllSyncerViews(ctx, c.gvr, deployment)
	if err != nil {
		return err
	}

	var emptyStatus appsv1.DeploymentStatus
	consolidatedConditions := make(map[appsv1.DeploymentConditionType]appsv1.DeploymentCondition)

	for _, syncerView := range syncerViews {
		if reflect.DeepEqual(syncerView.Status, emptyStatus) {
			continue
		}

		summarizedStatus.Replicas += syncerView.Status.Replicas
		summarizedStatus.UpdatedReplicas += syncerView.Status.UpdatedReplicas
		summarizedStatus.ReadyReplicas += syncerView.Status.ReadyReplicas
		summarizedStatus.AvailableReplicas += syncerView.Status.AvailableReplicas
		summarizedStatus.UnavailableReplicas += syncerView.Status.UnavailableReplicas

		for _, condition := range syncerView.Status.Conditions {
			if consolidated, ok := consolidatedConditions[condition.Type]; !ok {
				consolidatedConditions[condition.Type] = condition
			} else {
				switch consolidated.Status {
				case corev1.ConditionUnknown:
					consolidatedConditions[condition.Type] = condition
				case corev1.ConditionFalse:
					if condition.Status == corev1.ConditionTrue {
						consolidatedConditions[condition.Type] = condition
					}
				}
			}
		}
	}

	conditionTypes := sets.NewString()
	for conditionType := range consolidatedConditions {
		conditionTypes.Insert(string(conditionType))
	}

	for _, condition := range conditionTypes.List() {
		summarizedStatus.Conditions = append(summarizedStatus.Conditions, consolidatedConditions[appsv1.DeploymentConditionType(condition)])
	}

	return c.committer(clusterName, namespace)(ctx,
		&committer.Resource[*appsv1.DeploymentSpec, *appsv1.DeploymentStatus]{
			ObjectMeta: deployment.ObjectMeta,
			Status:     &deployment.Status,
		},
		&committer.Resource[*appsv1.DeploymentSpec, *appsv1.DeploymentStatus]{
			ObjectMeta: deployment.ObjectMeta,
			Status:     &summarizedStatus,
		})
}
