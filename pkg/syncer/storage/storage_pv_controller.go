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

package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	PersistentVolumeControllerName = "kcp-workload-syncer-storage-pv"
)

type PersistentVolumeController struct {
	queue                                 workqueue.RateLimitingInterface
	ddsifForUpstreamUpsyncer              *ddsif.DiscoveringDynamicSharedInformerFactory
	ddsifForDownstream                    *ddsif.GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister, informers.GenericInformer]
	syncTarget                            syncTargetSpec
	updateDownstreamPersistentVolumeClaim func(ctx context.Context, persistentVolumeClaim *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, error)
	getDownstreamPersistentVolume         func(ctx context.Context, name string) (runtime.Object, error)
	getDownstreamPersistentVolumeClaim    func(persistentVolumeClaimName, persistentVolumeClaimNamespace string) (runtime.Object, error)
	getUpstreamPersistentVolume           func(clusterName logicalcluster.Name, persistentVolumeName string) (runtime.Object, error)
}

// NewPersistentVolumeController returns a new storage persistent volume syncer controller.
func NewPersistentVolumeController(
	syncerLogger logr.Logger,
	syncTargetWorkspace logicalcluster.Name,
	syncTargetName, syncTargetKey string,
	downstreamKubeClient *kubernetes.Clientset,
	ddsifForUpstreamUpsyncer *ddsif.DiscoveringDynamicSharedInformerFactory,
	ddsifForDownstream *ddsif.GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister, informers.GenericInformer],
	syncTargetUID types.UID,
) (*PersistentVolumeController, error) {
	c := &PersistentVolumeController{
		queue:                    workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), PersistentVolumeControllerName),
		ddsifForUpstreamUpsyncer: ddsifForUpstreamUpsyncer,
		ddsifForDownstream:       ddsifForDownstream,
		syncTarget: syncTargetSpec{
			name:      syncTargetName,
			workspace: syncTargetWorkspace,
			uid:       syncTargetUID,
			key:       syncTargetKey,
		},
		updateDownstreamPersistentVolumeClaim: func(ctx context.Context, persistentVolumeClaim *corev1.PersistentVolumeClaim) (*corev1.PersistentVolumeClaim, error) {
			return downstreamKubeClient.CoreV1().PersistentVolumeClaims(persistentVolumeClaim.Namespace).Update(ctx, persistentVolumeClaim, metav1.UpdateOptions{})
		},
		getDownstreamPersistentVolumeClaim: func(persistentVolumeClaimName, persistentVolumeClaimNamespace string) (runtime.Object, error) {
			informer, err := ddsifForDownstream.ForResource(persistentVolumeClaimSchemeGroupVersion)
			if err != nil {
				return nil, err
			}

			pvc, err := informer.Lister().ByNamespace(persistentVolumeClaimNamespace).Get(persistentVolumeClaimName)
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}

			return pvc, nil
		},
		getUpstreamPersistentVolume: func(clusterName logicalcluster.Name, persistentVolumeName string) (runtime.Object, error) {
			informer, err := ddsifForUpstreamUpsyncer.ForResource(persistentVolumeSchemeGroupVersion)
			if err != nil {
				return nil, err
			}

			pv, err := informer.Lister().ByCluster(clusterName).Get(persistentVolumeName)
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}

			return pv, nil
		},
		getDownstreamPersistentVolume: func(ctx context.Context, name string) (runtime.Object, error) {
			informer, err := ddsifForDownstream.ForResource(persistentVolumeSchemeGroupVersion)
			if err != nil {
				return nil, err
			}

			pv, err := informer.Lister().Get(name)
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}

			return pv, nil
		},
	}

	logger := logging.WithReconciler(syncerLogger, PersistentVolumeControllerName)

	// Add the PV informer to the controller to react to upstream PV events.
	logger.V(2).Info("Setting upstream up informer", "gvr", persistentVolumeSchemeGroupVersion.String())

	informers, _ := ddsifForUpstreamUpsyncer.Informers()
	pvInformer, ok := informers[persistentVolumeSchemeGroupVersion]
	if !ok {
		return nil, fmt.Errorf("informer for %s not found", persistentVolumeSchemeGroupVersion.String())
	}

	pvInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.AddToQueue(obj, logger)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.AddToQueue(newObj, logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.AddToQueue(obj, logger)
		},
	})

	return c, nil
}

func (c *PersistentVolumeController) AddToQueue(obj interface{}, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing", "key", key)
	c.queue.Add(key)
}

// Start starts N worker processes processing work items.
func (c *PersistentVolumeController) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), PersistentVolumeControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting syncer workers")
	defer logger.Info("Stopping syncer workers")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

// startWorker processes work items until stopCh is closed.
func (c *PersistentVolumeController) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *PersistentVolumeController) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	key, quit := c.queue.Get()
	if quit {
		return false
	}

	qk := key.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), qk)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing", qk)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, qk); err != nil {
		utilruntime.HandleError(fmt.Errorf("%s failed to sync %q, err: %w", PersistentVolumeControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}
