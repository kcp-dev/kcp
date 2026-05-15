/*
Copyright 2025 The kcp Authors.

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

package logicalclustercleanup

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/logging"
	apibindingreconciler "github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
)

const (
	ControllerName = "kcp-logicalclustercleanup"
)

// NewController returns a new controller for LogicalCluster resource binding annotation cleanup.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
) (*controller, error) {
	c := &controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		getLogicalCluster: func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return logicalClusterInformer.Lister().Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
		},
		updateLogicalClusterCRDs: func(ctx context.Context, lc *corev1alpha1.LogicalCluster) error {
			// Use SSA to write only to the CRDs annotation key
			patchObj := &corev1alpha1.LogicalCluster{
				TypeMeta: metav1.TypeMeta{
					APIVersion: corev1alpha1.SchemeGroupVersion.String(),
					Kind:       "LogicalCluster",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name: lc.Name,
					Annotations: map[string]string{
						apibindingreconciler.LocksCRDsAnnotationKey: lc.Annotations[apibindingreconciler.LocksCRDsAnnotationKey],
					},
				},
			}
			patchBytes, err := json.Marshal(patchObj)
			if err != nil {
				return err
			}
			_, err = kcpClusterClient.CoreV1alpha1().Cluster(logicalcluster.From(lc).Path()).LogicalClusters().Patch(
				ctx,
				lc.Name,
				types.ApplyPatchType,
				patchBytes,
				metav1.PatchOptions{
					FieldManager: apibindingreconciler.FieldManagerCRDs,
					// Force is not needed because each writer uses its own annotation key
				},
			)
			return err
		},
		listsCRDs: func(clusterName logicalcluster.Name) ([]*apiextensionsv1.CustomResourceDefinition, error) {
			return crdInformer.Lister().Cluster(clusterName).List(labels.Everything())
		},
		listAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
			return apiBindingInformer.Lister().Cluster(clusterName).List(labels.Everything())
		},
	}

	_, _ = logicalClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueLogicalCluster(obj.(*corev1alpha1.LogicalCluster))
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueLogicalCluster(obj.(*corev1alpha1.LogicalCluster))
		},
	})

	_, _ = crdInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueCRD(obj.(*apiextensionsv1.CustomResourceDefinition), false)
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueCRD(obj.(*apiextensionsv1.CustomResourceDefinition), false)
		},
		DeleteFunc: func(obj interface{}) {
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			c.enqueueCRD(obj.(*apiextensionsv1.CustomResourceDefinition), true)
		},
	})

	_, _ = apiBindingInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueFromAPIBinding(obj.(*apisv1alpha2.APIBinding))
		},
		DeleteFunc: func(obj interface{}) {
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			c.enqueueFromAPIBinding(obj.(*apisv1alpha2.APIBinding))
		},
	}))

	return c, nil
}

// controller deletes bound CRDs when they are no longer in use by any APIBindings.
type controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	getLogicalCluster        func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)
	updateLogicalClusterCRDs func(ctx context.Context, logicalCluster *corev1alpha1.LogicalCluster) error
	listsCRDs                func(clusterName logicalcluster.Name) ([]*apiextensionsv1.CustomResourceDefinition, error)
	listAPIBindings          func(clusterName logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error)
}

// enqueueLogicalCluster enqueues a LogicalCluster.
func (c *controller) enqueueLogicalCluster(lc *corev1alpha1.LogicalCluster) {
	if lc.Name != corev1alpha1.LogicalClusterName {
		// should not happen.
		return
	}

	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(lc)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing LogicalCluster")
	c.queue.Add(key)
}

// enqueueCRD enqueues a CRD.
func (c *controller) enqueueCRD(crd *apiextensionsv1.CustomResourceDefinition, deleted bool) {
	key := kcpcache.ToClusterAwareKey(logicalcluster.From(crd).String(), "", corev1alpha1.LogicalClusterName)
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing LogicalCluster because of CRD", "CRD", crd.Name)
	c.queue.Add(key)
}

func (c *controller) enqueueFromAPIBinding(binding *apisv1alpha2.APIBinding) {
	key := kcpcache.ToClusterAwareKey(logicalcluster.From(binding).String(), "", corev1alpha1.LogicalClusterName)
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing LogicalCluster because of APIBinding", "APIBinding", binding.Name)
	c.queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for range numThreads {
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
	key := k

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %#v, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *controller) process(ctx context.Context, key string) error {
	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return fmt.Errorf("failed to split key: %w", err)
	}

	lc, err := c.getLogicalCluster(clusterName)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil // object got deleted before we handled it.
		}
		return err
	}

	logger := logging.WithObject(klog.FromContext(ctx), lc)

	// decode existing CRD annotation - this controller only manages CRD entries.
	rbs := make(apibindingreconciler.ResourceBindingsAnnotation)
	annValue, found := lc.Annotations[apibindingreconciler.LocksCRDsAnnotationKey]
	if found {
		if err := utiljson.Unmarshal([]byte(annValue), &rbs); err != nil {
			logger.Error(err, "failed to unmarshal CRDs locks annotation, resetting")
			annValue = ""
		}
	}
	if rbs == nil {
		rbs = make(apibindingreconciler.ResourceBindingsAnnotation)
	}

	// get objects
	bindings, err := c.listAPIBindings(clusterName)
	if err != nil {
		return err
	}
	crds, err := c.listsCRDs(clusterName)
	if err != nil {
		return err
	}

	// migrate CRD entries from legacy annotation if needed
	// Only migrate if we don't have any CRD entries yet
	if len(rbs) == 0 {
		// Check if there's a legacy annotation with CRD entries to migrate
		legacyAnn, legacyFound := lc.Annotations[apibindingreconciler.ResourceBindingsAnnotationKey] //nolint:staticcheck // SA1019 - using deprecated constant for backward compatibility during migration
		if legacyFound && legacyAnn != "" {
			var legacyRbs apibindingreconciler.ResourceBindingsAnnotation
			if err := utiljson.Unmarshal([]byte(legacyAnn), &legacyRbs); err == nil {
				for gr, lock := range legacyRbs {
					if lock.CRD {
						rbs[gr] = lock
					}
				}
			}
		}
	}

	// Build set of binding names for consistency checking
	bindingNames := make(map[string]bool)
	for _, b := range bindings {
		bindingNames[b.Name] = true
	}

	// Build set of established CRD names and group-resources
	crdNames := make(map[string]bool)
	crdGroupResources := make(map[string]bool)
	for _, crd := range crds {
		crdNames[crd.Name] = true

		if !apihelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established) {
			logger.V(4).Info("CRD is not established, skipping", "crd", crd.Name)
			continue
		}

		gr := schema.GroupResource{Group: crd.Spec.Group, Resource: crd.Spec.Names.Plural}
		crdGroupResources[gr.String()] = true
	}

	// Build new CRD entries map
	newRbs := make(apibindingreconciler.ResourceBindingsAnnotation)

	// First, add all established CRDs
	for gr := range crdGroupResources {
		newRbs[gr] = apibindingreconciler.ExpirableLock{Lock: apibindingreconciler.Lock{CRD: true}}
	}

	// Process existing CRD entries: preserve expiry for missing CRDs, clear expiry for existing ones
	for gr, lock := range rbs {
		if !lock.CRD {
			// Skip non-CRD entries - they shouldn't be here but just in case
			continue
		}
		if crdGroupResources[gr] {
			// CRD exists and is established, it's already in newRbs above
			continue
		}

		// CRD doesn't exist anymore
		if lock.CRDExpiry != nil && time.Now().After(lock.CRDExpiry.Time) {
			logger.V(4).Info("removing expired CRD binding of non-existing CRD", "crd", gr)
			// Don't add to newRbs (effectively deleting)
		} else {
			// Preserve the entry with its expiry
			newRbs[gr] = lock
		}
	}

	// update CRD annotation on LogicalCluster using SSA
	bs, err := utiljson.Marshal(newRbs)
	if err != nil {
		return fmt.Errorf("failed to marshal CRDs locks annotation: %w", err)
	}
	if lc.Annotations[apibindingreconciler.LocksCRDsAnnotationKey] == string(bs) {
		return nil
	}
	logger.V(4).Info("Updating "+apibindingreconciler.LocksCRDsAnnotationKey, "old", annValue, "new", string(bs))
	lc = lc.DeepCopy()
	if lc.Annotations == nil {
		lc.Annotations = make(map[string]string)
	}
	lc.Annotations[apibindingreconciler.LocksCRDsAnnotationKey] = string(bs)
	if err := c.updateLogicalClusterCRDs(ctx, lc); err != nil {
		return fmt.Errorf("failed to update LogicalCluster: %w", err)
	}

	return nil
}
