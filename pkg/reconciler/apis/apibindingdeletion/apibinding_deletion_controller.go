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

package apibindingdeletion

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	kcpmetadata "github.com/kcp-dev/client-go/metadata"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	apisv1alpha1client "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/apis/v1alpha1"
	apisv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/workspacedeletion/deletion"
)

const (
	ControllerName = "kcp-apibindingdeletion"

	APIBindingFinalizer = "apis.kcp.dev/apibinding-finalizer"

	DeletionRecheckEstimateSeconds = 5

	// ResourceDeletionFailedReason is the reason for condition BindingResourceDeleteSuccess that deletion of
	// some CRs is failed
	ResourceDeletionFailedReason = "ResourceDeletionFailed"

	// ResourceRemainingReason is the reason for condition BindingResourceDeleteSuccess that some CR resource still
	// exists when apibinding is deleting
	ResourceRemainingReason = "SomeResourcesRemain"

	// ResourceFinalizersRemainReason is the reason for condition BindingResourceDeleteSuccess that finalizers on some
	// CRs still exist.
	ResourceFinalizersRemainReason = "SomeFinalizersRemain"
)

func NewController(
	metadataClient kcpmetadata.ClusterInterface,
	kcpClusterClient kcpclientset.ClusterInterface,
	apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
) *Controller {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName)

	c := &Controller{
		queue: queue,
		listResources: func(ctx context.Context, cluster logicalcluster.Path, gvr schema.GroupVersionResource) (*metav1.PartialObjectMetadataList, error) {
			return metadataClient.Cluster(cluster).Resource(gvr).Namespace(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		},
		deleteResources: func(ctx context.Context, cluster logicalcluster.Path, gvr schema.GroupVersionResource, namespace string) error {
			background := metav1.DeletePropagationBackground
			opts := metav1.DeleteOptions{PropagationPolicy: &background}
			return metadataClient.Cluster(cluster).Resource(gvr).Namespace(namespace).DeleteCollection(ctx, opts, metav1.ListOptions{})
		},
		getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIBinding, error) {
			return apiBindingInformer.Lister().Cluster(cluster.Path()).Get(name)
		},
		commit: committer.NewCommitter[*APIBinding, Patcher, *APIBindingSpec, *APIBindingStatus](kcpClusterClient.ApisV1alpha1().APIBindings()),
	}

	apiBindingInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch obj := obj.(type) {
			case *apisv1alpha1.APIBinding:
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

type APIBinding = apisv1alpha1.APIBinding
type APIBindingSpec = apisv1alpha1.APIBindingSpec
type APIBindingStatus = apisv1alpha1.APIBindingStatus
type Patcher = apisv1alpha1client.APIBindingInterface
type Resource = committer.Resource[*APIBindingSpec, *APIBindingStatus]
type CommitFunc = func(context.Context, *Resource, *Resource) error

type Controller struct {
	queue workqueue.RateLimitingInterface

	listResources   func(ctx context.Context, cluster logicalcluster.Path, gvr schema.GroupVersionResource) (*metav1.PartialObjectMetadataList, error)
	deleteResources func(ctx context.Context, cluster logicalcluster.Path, gvr schema.GroupVersionResource, namespace string) error

	getAPIBinding func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIBinding, error)
	commit        CommitFunc
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(2).Info("queueing APIBinding")
	c.queue.Add(key)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

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
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

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
		logger.V(2).Info("custom resources remaining for APIBinding, waiting", "duration", duration)
		c.queue.AddAfter(key, duration)
	} else {
		// rather than wait for a full resync, re-add the workspace to the queue to be processed
		c.queue.AddRateLimited(key)
		runtime.HandleError(fmt.Errorf("deletion of apibinding %v failed: %w", key, err))
	}

	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	startTime := time.Now()
	cluster, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}
	clusterName := logicalcluster.Name(cluster.String()) // TODO: remove when SplitMetaClusterNamespaceKey is updated

	defer func() {
		logger.V(4).Info("finished syncing", "duration", time.Since(startTime))
	}()

	apibinding, deleteErr := c.getAPIBinding(clusterName, name)
	if apierrors.IsNotFound(deleteErr) {
		logger.V(3).Info("APIBinding has been deleted")
		return nil
	}
	if deleteErr != nil {
		runtime.HandleError(fmt.Errorf("unable to retrieve apibinding %v from store: %w", key, deleteErr))
		return deleteErr
	}
	logger = logging.WithObject(logger, apibinding)
	ctx = klog.NewContext(ctx, logger)

	if apibinding.DeletionTimestamp.IsZero() {
		return nil
	}

	oldResource := &Resource{ObjectMeta: apibinding.ObjectMeta, Spec: &apibinding.Spec, Status: &apibinding.Status}
	apibindingCopy := apibinding.DeepCopy()
	resourceRemaining, deleteErr := c.deleteAllCRs(ctx, apibindingCopy)
	if deleteErr != nil {
		conditions.MarkFalse(
			apibindingCopy,
			apisv1alpha1.BindingResourceDeleteSuccess,
			ResourceDeletionFailedReason,
			conditionsv1alpha1.ConditionSeverityError,
			deleteErr.Error(),
		)

		newResource := &Resource{ObjectMeta: apibindingCopy.ObjectMeta, Spec: &apibindingCopy.Spec, Status: &apibindingCopy.Status}
		if err := c.commit(ctx, oldResource, newResource); err != nil {
			return err
		}

		return deleteErr
	}

	apibindingCopy, remainingErr := c.mutateResourceRemainingStatus(resourceRemaining, apibindingCopy)
	if remainingErr != nil {
		newResource := &Resource{ObjectMeta: apibindingCopy.ObjectMeta, Spec: &apibindingCopy.Spec, Status: &apibindingCopy.Status}
		if err := c.commit(ctx, oldResource, newResource); err != nil {
			return err
		}

		return remainingErr
	}

	apibindingCopy = apibinding.DeepCopy()
	filtered := make([]string, 0, len(apibindingCopy.Finalizers))
	for i := range apibindingCopy.Finalizers {
		if apibindingCopy.Finalizers[i] == APIBindingFinalizer {
			continue
		}
		filtered = append(filtered, apibindingCopy.Finalizers[i])
	}
	if len(apibindingCopy.Finalizers) == len(filtered) {
		return nil
	}
	apibindingCopy.Finalizers = filtered
	logger.V(2).Info("finalizing APIBinding")
	newResource := &Resource{ObjectMeta: apibindingCopy.ObjectMeta, Spec: &apibindingCopy.Spec, Status: &apibindingCopy.Status}
	return c.commit(ctx, oldResource, newResource)
}

func (c *Controller) mutateResourceRemainingStatus(resourceRemaining gvrDeletionMetadataTotal, apibinding *apisv1alpha1.APIBinding) (*apisv1alpha1.APIBinding, error) {
	if len(resourceRemaining.finalizersToNumRemaining) != 0 {
		// requeue if there are still remaining finalizers
		remainingByFinalizer := []string{}
		for finalizer, numRemaining := range resourceRemaining.finalizersToNumRemaining {
			if numRemaining == 0 {
				continue
			}
			remainingByFinalizer = append(remainingByFinalizer, fmt.Sprintf("%s in %d resource instances", finalizer, numRemaining))
		}
		// sort for stable updates
		sort.Strings(remainingByFinalizer)
		conditions.MarkFalse(
			apibinding,
			apisv1alpha1.BindingResourceDeleteSuccess,
			ResourceFinalizersRemainReason,
			conditionsv1alpha1.ConditionSeverityError,
			fmt.Sprintf("Some content in the workspace has finalizers remaining: %s", strings.Join(remainingByFinalizer, ", ")),
		)

		return apibinding, &deletion.ResourcesRemainingError{
			Estimate: DeletionRecheckEstimateSeconds,
			Message:  fmt.Sprintf("finalizers %s remaining", strings.Join(remainingByFinalizer, ", ")),
		}
	}

	if len(resourceRemaining.gvrToNumRemaining) != 0 {
		// requeue if there are still remaining resources
		remainingResources := []string{}
		for gvr, numRemaining := range resourceRemaining.gvrToNumRemaining {
			if numRemaining == 0 {
				continue
			}
			remainingResources = append(remainingResources, fmt.Sprintf("%s.%s has %d resource instances", gvr.Resource, gvr.Group, numRemaining))
		}
		// sort for stable updates
		sort.Strings(remainingResources)

		conditions.MarkFalse(
			apibinding,
			apisv1alpha1.BindingResourceDeleteSuccess,
			ResourceRemainingReason,
			conditionsv1alpha1.ConditionSeverityError,
			fmt.Sprintf("Some resources are remaining: %s", strings.Join(remainingResources, ", ")),
		)

		return apibinding, &deletion.ResourcesRemainingError{
			Estimate: DeletionRecheckEstimateSeconds,
			Message:  fmt.Sprintf("resources %s remaining", strings.Join(remainingResources, ", ")),
		}
	}

	conditions.MarkTrue(apibinding, apisv1alpha1.BindingResourceDeleteSuccess)

	return apibinding, nil
}
