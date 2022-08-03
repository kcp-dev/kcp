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

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	specmutators "github.com/kcp-dev/kcp/pkg/syncer/spec/mutators"
	"github.com/kcp-dev/kcp/third_party/keyfunctions"
)

const (
	controllerName                   = "kcp-workload-syncer-spec"
	byNamespaceLocatorIndexName      = "syncer-spec-ByNamespaceLocator"
	byWorkspaceAndNamespaceIndexName = "syncer-spec-WorkspaceNamespace" // will go away with scoping
)

type Controller struct {
	queue workqueue.RateLimitingInterface

	mutators mutatorGvrMap

	upstreamClient                         dynamic.ClusterInterface
	downstreamClient                       dynamic.Interface
	upstreamInformers, downstreamInformers dynamicinformer.DynamicSharedInformerFactory

	syncTargetName            string
	syncTargetWorkspace       logicalcluster.Name
	syncTargetUID             types.UID
	syncTargetKey             string
	advancedSchedulingEnabled bool
}

func NewSpecSyncer(gvrs []schema.GroupVersionResource, syncTargetWorkspace logicalcluster.Name, syncTargetName, syncTargetKey string, upstreamURL *url.URL, advancedSchedulingEnabled bool,
	upstreamClient dynamic.ClusterInterface, downstreamClient dynamic.Interface, upstreamInformers, downstreamInformers dynamicinformer.DynamicSharedInformerFactory, syncTargetUID types.UID) (*Controller, error) {

	c := Controller{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName),

		upstreamClient:      upstreamClient,
		downstreamClient:    downstreamClient,
		upstreamInformers:   upstreamInformers,
		downstreamInformers: downstreamInformers,

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

	for _, gvr := range gvrs {
		gvr := gvr // because used in closure

		upstreamInformers.ForResource(gvr).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.AddToQueue(gvr, obj)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				oldUnstrob := oldObj.(*unstructured.Unstructured)
				newUnstrob := newObj.(*unstructured.Unstructured)

				if !deepEqualApartFromStatus(oldUnstrob, newUnstrob) {
					c.AddToQueue(gvr, newUnstrob)
				}
			},
			DeleteFunc: func(obj interface{}) {
				c.AddToQueue(gvr, obj)
			},
		})

		klog.V(2).InfoS("Set up upstream informer", "syncTargetWorkspace", syncTargetWorkspace, "syncTargetName", syncTargetName, "syncTargetKey", syncTargetKey, "gvr", gvr.String())

		downstreamInformers.ForResource(gvr).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			DeleteFunc: func(obj interface{}) {
				key, err := keyfunctions.DeletionHandlingMetaNamespaceKeyFunc(obj)
				if err != nil {
					utilruntime.HandleError(fmt.Errorf("error getting key for type %T: %w", obj, err))
					return
				}
				namespace, name, err := cache.SplitMetaNamespaceKey(key)
				if err != nil {
					utilruntime.HandleError(fmt.Errorf("error splitting key %q: %w", key, err))
				}
				klog.V(3).InfoS("processing  delete event", "key", key, "gvr", gvr, "namespace", namespace, "name", name)

				// Use namespace lister
				nsObj, err := namespaceLister.Get(namespace)
				if err != nil {
					utilruntime.HandleError(err)
					return
				}
				ns, ok := nsObj.(*unstructured.Unstructured)
				if !ok {
					utilruntime.HandleError(fmt.Errorf("unexpected object type: %T", nsObj))
					return
				}
				locator, ok := ns.GetAnnotations()[shared.NamespaceLocatorAnnotation]
				if !ok {
					utilruntime.HandleError(fmt.Errorf("unable to find the locator annotation in namespace %s", namespace))
					return
				}
				nsLocator := &shared.NamespaceLocator{}
				err = json.Unmarshal([]byte(locator), nsLocator)
				if err != nil {
					utilruntime.HandleError(err)
					return
				}
				klog.V(4).InfoS("found", "NamespaceLocator", nsLocator)
				m := &metav1.ObjectMeta{
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: nsLocator.Workspace.String(),
					},
					Namespace: nsLocator.Namespace,
					Name:      name,
				}
				c.AddToQueue(gvr, m)
			},
		})
		klog.V(2).InfoS("Set up downstream informer", "SyncTarget Workspace", syncTargetWorkspace, "SyncTarget Name", syncTargetName, "gvr", gvr.String())
	}

	secretMutator := specmutators.NewSecretMutator()

	upstreamSecretIndexer := upstreamInformers.ForResource(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}).Informer().GetIndexer()
	deploymentMutator := specmutators.NewDeploymentMutator(upstreamURL, newSecretLister(upstreamSecretIndexer))

	if err := upstreamSecretIndexer.AddIndexers(cache.Indexers{
		byWorkspaceAndNamespaceIndexName: indexByWorkspaceAndNamespace,
	}); err != nil {
		return nil, err
	}
	c.mutators = mutatorGvrMap{
		deploymentMutator.GVR(): deploymentMutator.Mutate,
		secretMutator.GVR():     secretMutator.Mutate,
	}

	return &c, nil
}

type queueKey struct {
	gvr schema.GroupVersionResource
	key string // meta namespace key
}

func (c *Controller) AddToQueue(gvr schema.GroupVersionResource, obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	klog.Infof("%s queueing GVR %q %s", controllerName, gvr.String(), key)
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

	klog.InfoS("Starting syncer workers", "controller", controllerName)
	defer klog.InfoS("Stopping syncer workers", "controller", controllerName)
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

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, qk.gvr, qk.key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%s failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}

func newSecretLister(secretIndexer cache.Indexer) specmutators.ListSecretFunc {
	return func(clusterName logicalcluster.Name, namespace string) ([]*unstructured.Unstructured, error) {
		secretList, err := secretIndexer.ByIndex(byWorkspaceAndNamespaceIndexName, workspaceAndNamespaceIndexKey(clusterName, namespace))
		if err != nil {
			return nil, fmt.Errorf("error listing secrets for workspace %s: %w", clusterName, err)
		}
		secrets := make([]*unstructured.Unstructured, 0, len(secretList))
		for _, elem := range secretList {
			unstrSecret := elem.(*unstructured.Unstructured)
			secrets = append(secrets, unstrSecret)
		}
		return secrets, nil
	}
}

func indexByWorkspaceAndNamespace(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}
	lcluster := logicalcluster.From(metaObj)
	return []string{workspaceAndNamespaceIndexKey(lcluster, metaObj.GetNamespace())}, nil
}

func workspaceAndNamespaceIndexKey(logicalcluster logicalcluster.Name, namespace string) string {
	return logicalcluster.String() + "/" + namespace
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
