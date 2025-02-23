/*
Copyright 2025 The KCP Authors.

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
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	apibindingreconciler "github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	"github.com/kcp-dev/kcp/pkg/reconciler/events"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	apisv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
)

const (
	ControllerName = "kcp-logicalclustercleanup"
)

// NewController returns a new controller for LogicalCluster resource binding annotation cleanup.
func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
	apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
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
		updateLogicalCluster: func(ctx context.Context, lc *corev1alpha1.LogicalCluster) error {
			_, err := kcpClusterClient.CoreV1alpha1().Cluster(logicalcluster.From(lc).Path()).LogicalClusters().Update(ctx, lc, metav1.UpdateOptions{})
			return err
		},
		listsCRDs: func(clusterName logicalcluster.Name) ([]*apiextensionsv1.CustomResourceDefinition, error) {
			return crdInformer.Lister().Cluster(clusterName).List(labels.Everything())
		},
		listAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
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
		DeleteFunc: func(obj interface{}) {
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			c.enqueueCRD(obj.(*apiextensionsv1.CustomResourceDefinition), true)
		},
	})

	_, _ = apiBindingInformer.Informer().AddEventHandler(events.WithoutSyncs(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueFromAPIBinding(obj.(*apisv1alpha1.APIBinding))
		},
		DeleteFunc: func(obj interface{}) {
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			c.enqueueFromAPIBinding(obj.(*apisv1alpha1.APIBinding))
		},
	}))

	return c, nil
}

// controller deletes bound CRDs when they are no longer in use by any APIBindings.
type controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	getLogicalCluster    func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)
	updateLogicalCluster func(ctx context.Context, logicalCluster *corev1alpha1.LogicalCluster) error
	listsCRDs            func(clusterName logicalcluster.Name) ([]*apiextensionsv1.CustomResourceDefinition, error)
	listAPIBindings      func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)
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

func (c *controller) enqueueFromAPIBinding(binding *apisv1alpha1.APIBinding) {
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

	// decode existing annotation.
	rbs := make(apibindingreconciler.ResourceBindingsAnnotation)
	annValue, found := lc.Annotations[apibindingreconciler.ResourceBindingsAnnotationKey]
	if found {
		if err := json.Unmarshal([]byte(annValue), &rbs); err != nil {
			logger.Error(err, "failed to unmarshal ResourceBindings annotation, resetting")
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

	// migrate or rebuild APIBinding entries from status.boundResources? This happens only once per logical cluster.
	// After the initial migration this is done by the apibinding controller.
	if annValue == "" {
		for _, b := range bindings {
			for _, br := range b.Status.BoundResources {
				gr := schema.GroupResource{Group: br.Group, Resource: br.Resource}
				if old, found := rbs[gr.String()]; !found {
					rbs[gr.String()] = apibindingreconciler.ExpirableLock{Lock: apibindingreconciler.Lock{Name: b.Name}}
				} else {
					logger.Info("resource is already bound to APIBinding. THIS ININCONSISTENT!", "resource", gr.String(), "apibinding", old.Name, "apibinding", b.Name)
				}
			}
		}
	} else {
		// remove bindings that are gone.
		bindingNames := make(map[string]bool)
		for _, b := range bindings {
			bindingNames[b.Name] = true
		}
		for gr, b := range rbs {
			if b.CRD {
				continue
			}
			if _, found := bindingNames[b.Name]; !found {
				logger.V(4).Info("removing binding", "binding", b.Name, "resource", gr)
				delete(rbs, gr)
			}
		}
	}

	// always add CRDs.
	crdNames := make(map[string]bool)
	for _, crd := range crds {
		crdNames[crd.Name] = true

		if !apihelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established) {
			continue
		}

		gr := schema.GroupResource{Group: crd.Spec.Group, Resource: crd.Spec.Names.Plural}
		if old, found := rbs[gr.String()]; !found {
			rbs[gr.String()] = apibindingreconciler.ExpirableLock{Lock: apibindingreconciler.Lock{CRD: true}}
		} else if !old.CRD {
			logger.Info("CRD exists and is established, but already bound to APIBinding. THIS IS INCONSISTENT!", "resource", gr.String(), "apibinding", old.Name)
		}
	}

	// remove only when expired and gone, remove expiry when CRD exists.
	for gr, b := range rbs {
		if !b.CRD {
			continue
		}
		if crdNames[gr] {
			b.CRDExpiry = nil
			rbs[gr] = b
			continue
		}

		// CRD doesn't exist.
		if b.CRDExpiry != nil && time.Now().After(b.CRDExpiry.Time) {
			logger.V(4).Info("removing expired CRD binding of non-existing CRD", "crd", gr)
			delete(rbs, gr)
		}
	}

	// update annotation on LogicalCluster.
	bs, err := json.Marshal(rbs)
	if err != nil {
		return fmt.Errorf("failed to marshal ResourceBindings annotation: %w", err)
	}
	if lc.Annotations[apibindingreconciler.ResourceBindingsAnnotationKey] == string(bs) {
		return nil
	}
	logger.V(4).Info("Updating"+apibindingreconciler.ResourceBindingsAnnotationKey, "old", annValue, "new", string(bs))
	lc = lc.DeepCopy()
	if lc.Annotations == nil {
		lc.Annotations = make(map[string]string)
	}
	lc.Annotations[apibindingreconciler.ResourceBindingsAnnotationKey] = string(bs)
	if err := c.updateLogicalCluster(ctx, lc); err != nil {
		return fmt.Errorf("failed to update LogicalCluster: %w", err)
	}

	return nil
}
