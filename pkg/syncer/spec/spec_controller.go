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
	"fmt"
	"net/url"
	"time"

	"github.com/go-logr/logr"
	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpdynamicinformer "github.com/kcp-dev/client-go/dynamic/dynamicinformer"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listersappsv1 "k8s.io/client-go/listers/apps/v1"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	listersrbacv1 "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/resourcesync"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	"github.com/kcp-dev/kcp/pkg/syncer/spec/dns"
	specmutators "github.com/kcp-dev/kcp/pkg/syncer/spec/mutators"
	. "github.com/kcp-dev/kcp/tmc/pkg/logging"
)

const (
	controllerName              = "kcp-workload-syncer-spec"
	byNamespaceLocatorIndexName = "syncer-spec-ByNamespaceLocator"
)

type Controller struct {
	queue workqueue.RateLimitingInterface

	mutators     mutatorGvrMap
	dnsProcessor *dns.DNSProcessor

	upstreamClient            kcpdynamic.ClusterInterface
	downstreamClient          dynamic.Interface
	syncerInformers           resourcesync.SyncerInformerFactory
	downstreamNSInformer      informers.GenericInformer
	downstreamNSCleaner       shared.Cleaner
	syncTargetName            string
	syncTargetWorkspace       logicalcluster.Path
	syncTargetUID             types.UID
	syncTargetKey             string
	advancedSchedulingEnabled bool
}

func NewSpecSyncer(syncerLogger logr.Logger, syncTargetWorkspace logicalcluster.Path, syncTargetName, syncTargetKey string,
	upstreamURL *url.URL, advancedSchedulingEnabled bool,
	upstreamClient kcpdynamic.ClusterInterface, downstreamClient dynamic.Interface, downstreamKubeClient kubernetes.Interface,
	upstreamInformers kcpdynamicinformer.DynamicSharedInformerFactory, downstreamInformers dynamicinformer.DynamicSharedInformerFactory, downstreamNSCleaner shared.Cleaner,
	syncerInformers resourcesync.SyncerInformerFactory,
	syncTargetUID types.UID,
	serviceAccountLister listerscorev1.ServiceAccountLister,
	roleLister listersrbacv1.RoleLister,
	roleBindingLister listersrbacv1.RoleBindingLister,
	deploymentLister listersappsv1.DeploymentLister,
	serviceLister listerscorev1.ServiceLister,
	endpointLister listerscorev1.EndpointsLister,
	dnsNamespace string,
	dnsImage string) (*Controller, error) {

	c := Controller{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),

		upstreamClient:      upstreamClient,
		downstreamClient:    downstreamClient,
		downstreamNSCleaner: downstreamNSCleaner,

		syncerInformers:           syncerInformers,
		syncTargetName:            syncTargetName,
		syncTargetWorkspace:       syncTargetWorkspace,
		syncTargetUID:             syncTargetUID,
		syncTargetKey:             syncTargetKey,
		advancedSchedulingEnabled: advancedSchedulingEnabled,
	}

	namespaceGVR := schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "namespaces",
	}
	namespaceLister := downstreamInformers.ForResource(namespaceGVR).Lister()

	err := downstreamInformers.ForResource(namespaceGVR).Informer().AddIndexers(cache.Indexers{byNamespaceLocatorIndexName: indexByNamespaceLocator})
	if err != nil {
		return nil, err
	}
	c.downstreamNSInformer = downstreamInformers.ForResource(namespaceGVR)

	logger := logging.WithReconciler(syncerLogger, controllerName)

	syncerInformers.AddUpstreamEventHandler(
		func(gvr schema.GroupVersionResource) cache.ResourceEventHandler {
			logger.V(2).Info("Set up upstream informer", "gvr", gvr.String())
			return cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					c.AddToQueue(gvr, obj, logger)
				},
				UpdateFunc: func(oldObj, newObj interface{}) {
					oldUnstrob := oldObj.(*unstructured.Unstructured)
					newUnstrob := newObj.(*unstructured.Unstructured)

					if !deepEqualApartFromStatus(logger, oldUnstrob, newUnstrob) {
						c.AddToQueue(gvr, newUnstrob, logger)
					}
				},
				DeleteFunc: func(obj interface{}) {
					c.AddToQueue(gvr, obj, logger)
				},
			}
		})

	syncerInformers.AddDownstreamEventHandler(
		func(gvr schema.GroupVersionResource) cache.ResourceEventHandler {
			logger.V(2).Info("Set up downstream informer", "gvr", gvr.String())
			return cache.ResourceEventHandlerFuncs{
				DeleteFunc: func(obj interface{}) {
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
						nsObj, err := namespaceLister.Get(namespace)
						if errors.IsNotFound(err) {
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
							logicalcluster.AnnotationKey: nsLocator.Workspace.String(),
						},
						Namespace: nsLocator.Namespace,
						Name:      shared.GetUpstreamResourceName(gvr, name),
					}
					c.AddToQueue(gvr, m, logger)
				},
			}
		})

	secretMutator := specmutators.NewSecretMutator()

	// make sure the secrets informer gets started
	_ = upstreamInformers.ForResource(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}).Informer()
	deploymentMutator := specmutators.NewDeploymentMutator(upstreamURL, func(clusterName logicalcluster.Path, namespace string) ([]runtime.Object, error) {
		return upstreamInformers.ForResource(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}).Lister().ByCluster(clusterName).ByNamespace(namespace).List(labels.Everything())
	}, serviceLister, syncTargetWorkspace, syncTargetUID, syncTargetName, dnsNamespace)

	c.mutators = mutatorGvrMap{
		deploymentMutator.GVR(): deploymentMutator.Mutate,
		secretMutator.GVR():     secretMutator.Mutate,
	}

	c.dnsProcessor = dns.NewDNSProcessor(downstreamKubeClient, serviceAccountLister, roleLister, roleBindingLister, deploymentLister,
		serviceLister, endpointLister, syncTargetName, syncTargetUID, dnsNamespace, dnsImage)

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

// indexByNamespaceLocator is a cache.IndexFunc that indexes namespaces by the namespaceLocator annotation.
func indexByNamespaceLocator(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}
	if loc, found, err := shared.LocatorFromAnnotations(metaObj.GetAnnotations()); err != nil {
		return []string{}, fmt.Errorf("failed to get locator from annotations: %w", err)
	} else if !found {
		return []string{}, nil
	} else {
		bs, err := json.Marshal(loc)
		if err != nil {
			return []string{}, fmt.Errorf("failed to marshal locator %#v: %w", loc, err)
		}
		return []string{string(bs)}, nil
	}
}
