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
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apisinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	apislisters "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/clusterworkspacedeletion/deletion"
)

const (
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
	metadataClient metadata.Interface,
	kcpClusterClient kcpclient.ClusterInterface,
	apiBindingInformer apisinformers.APIBindingInformer,
) *Controller {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "apibinding-deletion")

	c := &Controller{
		queue:             queue,
		metadataClient:    metadataClient,
		kcpClusterClient:  kcpClusterClient,
		apiBindingsLister: apiBindingInformer.Lister(),
	}

	apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})

	return c
}

type Controller struct {
	queue workqueue.RateLimitingInterface

	metadataClient   metadata.Interface
	kcpClusterClient kcpclient.ClusterInterface

	apiBindingsLister apislisters.APIBindingLister
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	klog.Infof("Queueing apibinding %q", key)
	c.queue.Add(key)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Info("Starting APIBinding Deletion controller")
	defer klog.Info("Shutting down APIBinding Deletion controller")

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

	klog.Infof("processing key %q", key)

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
		klog.V(2).Infof("custom resources remaining for apibinding %s, waiting %d seconds", key, t)
		c.queue.AddAfter(key, time.Duration(t)*time.Second)
	} else {
		// rather than wait for a full resync, re-add the workspace to the queue to be processed
		c.queue.AddRateLimited(key)
		runtime.HandleError(fmt.Errorf("deletion of apibinding %v failed: %w", key, err))
	}
	runtime.HandleError(fmt.Errorf("deletion of apibinding %v failed: %w", key, err))

	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	startTime := time.Now()

	defer func() {
		klog.V(4).Infof("Finished syncing apibinding %q (%v)", key, time.Since(startTime))
	}()

	apibinding, err := c.apiBindingsLister.Get(key)
	if apierrors.IsNotFound(err) {
		klog.Infof("apibinding has been deleted %v", key)
		return nil
	}
	if err != nil {
		runtime.HandleError(fmt.Errorf("unable to retrieve apibinding %v from store: %w", key, err))
		return err
	}

	apibindingCopy := apibinding.DeepCopy()
	if apibindingCopy.DeletionTimestamp.IsZero() {
		hasFinalizer := false
		for i := range apibindingCopy.Finalizers {
			if apibindingCopy.Finalizers[i] == APIBindingFinalizer {
				hasFinalizer = true
				break
			}
		}

		if hasFinalizer {
			return nil
		}

		apibindingCopy.Finalizers = append(apibindingCopy.Finalizers, APIBindingFinalizer)
		_, err := c.kcpClusterClient.Cluster(logicalcluster.From(apibindingCopy)).ApisV1alpha1().APIBindings().Update(
			ctx, apibindingCopy, metav1.UpdateOptions{})

		return err
	}

	resourceRemaining, err := c.deleteAllCRs(ctx, apibindingCopy)

	if err != nil {
		conditions.MarkFalse(
			apibindingCopy,
			apisv1alpha1.BindingResourceDeleteSuccess,
			ResourceDeletionFailedReason,
			conditionsv1alpha1.ConditionSeverityError,
			err.Error(),
		)

		if patchErr := c.patchCondition(ctx, apibinding, apibindingCopy); patchErr != nil {
			return patchErr
		}

		return err
	}

	apibindingCopy, err = c.mutateResourceRemainingStatus(resourceRemaining, apibindingCopy)

	if err != nil {
		if patchErr := c.patchCondition(ctx, apibinding, apibindingCopy); patchErr != nil {
			return patchErr
		}

		return err
	}

	return c.finalizeAPIBinding(ctx, apibindingCopy)
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

		return apibinding, &deletion.ResourcesRemainingError{Estimate: DeletionRecheckEstimateSeconds}
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

		return apibinding, &deletion.ResourcesRemainingError{Estimate: DeletionRecheckEstimateSeconds}
	}

	conditions.MarkTrue(apibinding, apisv1alpha1.BindingResourceDeleteSuccess)

	return apibinding, nil
}

func (c *Controller) patchCondition(ctx context.Context, old, new *apisv1alpha1.APIBinding) error {
	if equality.Semantic.DeepEqual(old.Status.Conditions, new.Status.Conditions) {
		return nil
	}

	oldData, err := json.Marshal(apisv1alpha1.APIBinding{
		Status: apisv1alpha1.APIBindingStatus{
			Conditions: old.Status.Conditions,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to Marshal old data for apibinding %s: %w", old.Name, err)
	}

	newData, err := json.Marshal(apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			UID:             old.UID,
			ResourceVersion: old.ResourceVersion,
		}, // to ensure they appear in the patch as preconditions
		Status: apisv1alpha1.APIBindingStatus{
			Conditions: new.Status.Conditions,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to Marshal new data for apibinding %s: %w", new.Name, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return fmt.Errorf("failed to create patch for apibinding %s: %w", new.Name, err)
	}

	_, err = c.kcpClusterClient.Cluster(logicalcluster.From(new)).ApisV1alpha1().APIBindings().Patch(ctx, new.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	return err
}

// finalizeAPIBinding removes the specified finalizer and finalizes the apibinding
func (c *Controller) finalizeAPIBinding(ctx context.Context, apibinding *apisv1alpha1.APIBinding) error {
	copiedFinalizers := []string{}
	for i := range apibinding.Finalizers {
		if apibinding.Finalizers[i] == APIBindingFinalizer {
			continue
		}
		copiedFinalizers = append(copiedFinalizers, APIBindingFinalizer)
	}
	if len(apibinding.Finalizers) == len(copiedFinalizers) {
		return nil
	}

	apibinding.Finalizers = copiedFinalizers
	_, err := c.kcpClusterClient.Cluster(logicalcluster.From(apibinding)).ApisV1alpha1().APIBindings().Update(
		ctx, apibinding, metav1.UpdateOptions{})

	return err
}
