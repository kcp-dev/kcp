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

package spec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	syncerindexers "github.com/kcp-dev/kcp/pkg/syncer/indexers"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	"github.com/kcp-dev/kcp/pkg/syncer/spec/dns"
	specmutators "github.com/kcp-dev/kcp/pkg/syncer/spec/mutators"
	. "github.com/kcp-dev/kcp/tmc/pkg/logging"
)

const (
	controllerName = "kcp-workload-syncer-spec"
)

var namespaceGVR schema.GroupVersionResource = corev1.SchemeGroupVersion.WithResource("namespaces")

type Controller struct {
	queue workqueue.RateLimitingInterface

	mutators     mutatorGvrMap
	dnsProcessor *dns.DNSProcessor

	upstreamClient   kcpdynamic.ClusterInterface
	downstreamClient dynamic.Interface

	getUpstreamLister                 func(gvr schema.GroupVersionResource) (kcpcache.GenericClusterLister, error)
	getDownstreamLister               func(gvr schema.GroupVersionResource) (cache.GenericLister, error)
	listDownstreamNamespacesByLocator func(jsonLocator string) ([]*unstructured.Unstructured, error)

	downstreamNSCleaner       shared.Cleaner
	syncTargetName            string
	syncTargetClusterName     logicalcluster.Name
	syncTargetUID             types.UID
	syncTargetKey             string
	advancedSchedulingEnabled bool
}

func NewSpecSyncer(syncerLogger logr.Logger, syncTargetClusterName logicalcluster.Name, syncTargetName, syncTargetKey string,
	upstreamURL *url.URL, advancedSchedulingEnabled bool,
	upstreamClient kcpdynamic.ClusterInterface, downstreamClient dynamic.Interface, downstreamKubeClient kubernetes.Interface,
	ddsifForUpstreamSyncer *ddsif.DiscoveringDynamicSharedInformerFactory,
	ddsifForDownstream *ddsif.GenericDiscoveringDynamicSharedInformerFactory[cache.SharedIndexInformer, cache.GenericLister, informers.GenericInformer],
	downstreamNSCleaner shared.Cleaner,
	syncTargetUID types.UID,
	dnsNamespace string,
	syncerNamespaceInformerFactory informers.SharedInformerFactory,
	dnsImage string) (*Controller, error) {
	c := Controller{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),

		upstreamClient:      upstreamClient,
		downstreamClient:    downstreamClient,
		downstreamNSCleaner: downstreamNSCleaner,

		getDownstreamLister: func(gvr schema.GroupVersionResource) (cache.GenericLister, error) {
			informers, notSynced := ddsifForDownstream.Informers()
			informer, ok := informers[gvr]
			if !ok {
				if shared.ContainsGVR(notSynced, gvr) {
					return nil, fmt.Errorf("informer for gvr %v not synced in the downstream informer factory", gvr)
				}
				return nil, fmt.Errorf("gvr %v should be known in the downstream informer factory", gvr)
			}
			return informer.Lister(), nil
		},
		getUpstreamLister: func(gvr schema.GroupVersionResource) (kcpcache.GenericClusterLister, error) {
			informers, notSynced := ddsifForUpstreamSyncer.Informers()
			informer, ok := informers[gvr]
			if !ok {
				if shared.ContainsGVR(notSynced, gvr) {
					return nil, fmt.Errorf("informer for gvr %v not synced in the downstream informer factory", gvr)
				}
				return nil, fmt.Errorf("gvr %v should be known in the downstream informer factory", gvr)
			}
			return informer.Lister(), nil
		},

		listDownstreamNamespacesByLocator: func(jsonLocator string) ([]*unstructured.Unstructured, error) {
			nsInformer, err := ddsifForDownstream.ForResource(namespaceGVR)
			if err != nil {
				return nil, err
			}
			return indexers.ByIndex[*unstructured.Unstructured](nsInformer.Informer().GetIndexer(), syncerindexers.ByNamespaceLocatorIndexName, jsonLocator)
		},
		syncTargetName:            syncTargetName,
		syncTargetClusterName:     syncTargetClusterName,
		syncTargetUID:             syncTargetUID,
		syncTargetKey:             syncTargetKey,
		advancedSchedulingEnabled: advancedSchedulingEnabled,
	}

	logger := logging.WithReconciler(syncerLogger, controllerName)

	namespaceGVR := corev1.SchemeGroupVersion.WithResource("namespaces")

	ddsifForUpstreamSyncer.AddEventHandler(
		ddsif.GVREventHandlerFuncs{
			AddFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
				if gvr == namespaceGVR {
					return
				}
				c.AddToQueue(gvr, obj, logger)
			},
			UpdateFunc: func(gvr schema.GroupVersionResource, oldObj, newObj interface{}) {
				if gvr == namespaceGVR {
					return
				}
				oldUnstrob := oldObj.(*unstructured.Unstructured)
				newUnstrob := newObj.(*unstructured.Unstructured)

				if !deepEqualApartFromStatus(logger, oldUnstrob, newUnstrob) {
					c.AddToQueue(gvr, newUnstrob, logger)
				}
			},
			DeleteFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
				if gvr == namespaceGVR {
					return
				}
				c.AddToQueue(gvr, obj, logger)
			},
		},
	)

	ddsifForDownstream.AddEventHandler(
		ddsif.GVREventHandlerFuncs{
			DeleteFunc: func(gvr schema.GroupVersionResource, obj interface{}) {
				if gvr == namespaceGVR {
					return
				}
				key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
				if err != nil {
					utilruntime.HandleError(fmt.Errorf("error getting key for type %T: %w", obj, err))
					return
				}
				namespace, name, err := cache.SplitMetaNamespaceKey(key)
				if err != nil {
					utilruntime.HandleError(fmt.Errorf("error splitting key %q: %w", key, err))
				}
				logger := logging.WithQueueKey(logger, key).WithValues("gvr", gvr, DownstreamNamespace, namespace, DownstreamName, name)
				logger.V(3).Info("processing delete event")

				var nsLocatorHolder *unstructured.Unstructured
				var ok bool
				// Handle namespaced resources
				if namespace != "" {
					// Use namespace lister
					namespaceLister, err := c.getDownstreamLister(namespaceGVR)
					if err != nil {
						utilruntime.HandleError(err)
						return
					}

					nsObj, err := namespaceLister.Get(namespace)
					if apierrors.IsNotFound(err) {
						return
					}
					if err != nil {
						utilruntime.HandleError(err)
						return
					}
					c.downstreamNSCleaner.PlanCleaning(namespace)
					nsLocatorHolder, ok = nsObj.(*unstructured.Unstructured)
					if !ok {
						utilruntime.HandleError(fmt.Errorf("unexpected object type: %T", nsObj))
						return
					}
				} else {
					// The nsLocatorHolder is in the resource itself for cluster-scoped resources.
					nsLocatorHolder, ok = obj.(*unstructured.Unstructured)
					if !ok {
						utilruntime.HandleError(fmt.Errorf("unexpected object type: %T", obj))
						return
					}
				}
				logger = logging.WithObject(logger, nsLocatorHolder)

				locator, ok := nsLocatorHolder.GetAnnotations()[shared.NamespaceLocatorAnnotation]
				if !ok {
					utilruntime.HandleError(fmt.Errorf("unable to find the locator annotation in resource %s", nsLocatorHolder.GetName()))
					return
				}
				nsLocator := &shared.NamespaceLocator{}
				err = json.Unmarshal([]byte(locator), nsLocator)
				if err != nil {
					utilruntime.HandleError(err)
					return
				}
				logger.V(4).Info("found", "NamespaceLocator", nsLocator)
				m := &metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: nsLocator.ClusterName.String(),
					},
					Namespace: nsLocator.Namespace,
					Name:      shared.GetUpstreamResourceName(gvr, name),
				}
				c.AddToQueue(gvr, m, logger)
			},
		})

	secretMutator := specmutators.NewSecretMutator()

	dnsServiceLister := syncerNamespaceInformerFactory.Core().V1().Services().Lister()

	deploymentMutator := specmutators.NewDeploymentMutator(upstreamURL, func(clusterName logicalcluster.Name, namespace string) ([]runtime.Object, error) {
		secretLister, err := c.getUpstreamLister(corev1.SchemeGroupVersion.WithResource("secrets"))
		if err != nil {
			return nil, errors.New("informer should be up and synced for namespaces in the upstream syncer informer factory")
		}
		return secretLister.ByCluster(clusterName).ByNamespace(namespace).List(labels.Everything())
	}, dnsServiceLister, syncTargetClusterName, syncTargetUID, syncTargetName, dnsNamespace)

	c.mutators = mutatorGvrMap{
		deploymentMutator.GVR(): deploymentMutator.Mutate,
		secretMutator.GVR():     secretMutator.Mutate,
	}

	c.dnsProcessor = dns.NewDNSProcessor(downstreamKubeClient,
		syncerNamespaceInformerFactory.Core().V1().ServiceAccounts().Lister(),
		syncerNamespaceInformerFactory.Rbac().V1().Roles().Lister(),
		syncerNamespaceInformerFactory.Rbac().V1().RoleBindings().Lister(),
		syncerNamespaceInformerFactory.Apps().V1().Deployments().Lister(),
		dnsServiceLister,
		syncerNamespaceInformerFactory.Core().V1().Endpoints().Lister(),
		syncerNamespaceInformerFactory.Networking().V1().NetworkPolicies().Lister(),
		syncTargetUID, syncTargetName, syncTargetKey, dnsNamespace, dnsImage)

	return &c, nil
}

type queueKey struct {
	gvr schema.GroupVersionResource
	key string // meta namespace key
}

func (c *Controller) AddToQueue(gvr schema.GroupVersionResource, obj interface{}, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(2).Info("queueing GVR", "gvr", gvr.String())
	c.queue.Add(
		queueKey{
			gvr: gvr,
			key: key,
		},
	)
}

// Start starts N worker processes processing work items.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), controllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting syncer workers")
	defer logger.Info("Stopping syncer workers")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

// startWorker processes work items until stopCh is closed.
func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	qk := key.(queueKey)

	logger := logging.WithQueueKey(klog.FromContext(ctx), qk.key).WithValues("gvr", qk.gvr)
	ctx = klog.NewContext(ctx, logger)
	logger.V(1).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if retryAfter, err := c.process(ctx, qk.gvr, qk.key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%s failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	} else if retryAfter != nil {
		c.queue.AddAfter(key, *retryAfter)
		return true
	}

	c.queue.Forget(key)

	return true
}
