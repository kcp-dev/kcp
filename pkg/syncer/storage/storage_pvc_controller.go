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
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
)

const PersistentVolumeClaimControllerName = "kcp-workload-syncer-storage-pvc"

var (
	persistentVolumeClaimSchemeGroupVersion = corev1.SchemeGroupVersion.WithResource("persistentvolumeclaims")
	persistentVolumeSchemeGroupVersion      = corev1.SchemeGroupVersion.WithResource("persistentvolumes")
	namespacesSchemeGroupVersion            = corev1.SchemeGroupVersion.WithResource("namespaces")
)

type PersistentVolumeClaimController struct {
	queue                              workqueue.RateLimitingInterface
	ddsifForUpstreamSyncer             *ddsif.DiscoveringDynamicSharedInformerFactory
	syncTarget                         syncTargetSpec
	updateDownstreamPersistentVolume   func(ctx context.Context, persistentVolume *corev1.PersistentVolume) (*corev1.PersistentVolume, error)
	getDownstreamPersistentVolumeClaim func(persistentVolumeClaimName, persistentVolumeClaimNamespace string) (runtime.Object, error)
	getDownstreamNamespace             func(name string) (runtime.Object, error)
	getDownstreamPersistentVolume      func(persistentVolumeName string) (runtime.Object, error)
	getUpstreamPersistentVolumeClaim   func(clusterName logicalcluster.Name, persistentVolumeClaimName, persistentVolumeClaimNamespace string) (runtime.Object, error)
	commit                             func(ctx context.Context, r *Resource, p *Resource, namespace string) error
}

// syncTargetSpec contains all the details about a given sync target.
type syncTargetSpec struct {
	name      string
	workspace logicalcluster.Name
	uid       types.UID
	key       string
}

type PersistentVolumeClaim = corev1.PersistentVolumeClaim
type PersistentVolumeClaimSpec = corev1.PersistentVolumeClaimSpec
type PersistentVolumeClaimStatus = corev1.PersistentVolumeClaimStatus
type Patcher = corev1client.PersistentVolumeClaimInterface
type Resource = committer.Resource[*PersistentVolumeClaimSpec, *PersistentVolumeClaimStatus]

// NewPersistentVolumeClaimController returns a new storage persistent volume claim syncer controller.
func NewPersistentVolumeClaimController(
	syncerLogger logr.Logger,
	syncTargetWorkspace logicalcluster.Name,
	syncTargetName, syncTargetKey string,
	downstreamKubeClient *kubernetes.Clientset,
	ddsifForUpstreamSyncer *ddsif.DiscoveringDynamicSharedInformerFactory,
	ddsifForDownstream *ddsif.GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister, informers.GenericInformer],
	syncTargetUID types.UID,
) (*PersistentVolumeClaimController, error) {
	c := &PersistentVolumeClaimController{
		queue:                  workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), PersistentVolumeClaimControllerName),
		ddsifForUpstreamSyncer: ddsifForUpstreamSyncer,
		syncTarget: syncTargetSpec{
			name:      syncTargetName,
			workspace: syncTargetWorkspace,
			uid:       syncTargetUID,
			key:       syncTargetKey,
		},
		updateDownstreamPersistentVolume: func(ctx context.Context, persistentVolume *corev1.PersistentVolume) (*corev1.PersistentVolume, error) {
			return downstreamKubeClient.CoreV1().PersistentVolumes().Update(ctx, persistentVolume, metav1.UpdateOptions{})
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
		getDownstreamPersistentVolume: func(persistentVolumeName string) (runtime.Object, error) {
			informer, err := ddsifForDownstream.ForResource(persistentVolumeSchemeGroupVersion)
			if err != nil {
				return nil, err
			}

			pv, err := informer.Lister().Get(persistentVolumeName)
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}

			return pv, nil
		},
		getDownstreamNamespace: func(name string) (runtime.Object, error) {
			informer, err := ddsifForDownstream.ForResource(namespacesSchemeGroupVersion)
			if err != nil {
				return nil, err
			}

			ns, err := informer.Lister().Get(name)
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}

			return ns, nil
		},
		getUpstreamPersistentVolumeClaim: func(clusterName logicalcluster.Name, persistentVolumeClaimName, persistentVolumeClaimNamespace string) (runtime.Object, error) {
			informer, err := ddsifForUpstreamSyncer.ForResource(persistentVolumeClaimSchemeGroupVersion)
			if err != nil {
				return nil, err
			}

			pv, err := informer.Lister().ByCluster(clusterName).ByNamespace(persistentVolumeClaimNamespace).Get(persistentVolumeClaimName)
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}

			return pv, nil
		},
		commit: func(ctx context.Context, r *Resource, p *Resource, namespace string) error {
			commitFunc := committer.NewCommitterScoped[*PersistentVolumeClaim, Patcher, *PersistentVolumeClaimSpec, *PersistentVolumeClaimStatus](downstreamKubeClient.CoreV1().PersistentVolumeClaims(namespace))
			return commitFunc(ctx, r, p)
		},
	}

	logger := logging.WithReconciler(syncerLogger, PersistentVolumeClaimControllerName)

	// Add the PVC informer to the controller to react to downstream PVC events.
	logger.V(2).Info("Setting up downstream informer", "gvr", persistentVolumeClaimSchemeGroupVersion.String())
	informers, _ := ddsifForDownstream.Informers()
	pvcInformer, ok := informers[persistentVolumeClaimSchemeGroupVersion]
	if !ok {
		return nil, fmt.Errorf("informer for %s not found", persistentVolumeClaimSchemeGroupVersion.String())
	}

	pvcInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
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

func (c *PersistentVolumeClaimController) AddToQueue(obj interface{}, logger logr.Logger) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing", "key", key)
	c.queue.Add(key)
}

// Start starts N worker processes processing work items.
func (c *PersistentVolumeClaimController) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), PersistentVolumeClaimControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting syncer workers")
	defer logger.Info("Stopping syncer workers")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

// startWorker processes work items until stopCh is closed.
func (c *PersistentVolumeClaimController) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *PersistentVolumeClaimController) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	key, quit := c.queue.Get()
	if quit {
		return false
	}

	qk := key.(string)

	logger := logging.WithQueueKey(klog.FromContext(ctx), qk)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing", "key", qk)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, qk); err != nil {
		utilruntime.HandleError(fmt.Errorf("%s failed to sync %q, err: %w", PersistentVolumeClaimControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}
