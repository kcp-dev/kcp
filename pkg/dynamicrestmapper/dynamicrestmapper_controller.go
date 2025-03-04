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

package dynamicrestmapper

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	ControllerName = "kcp-dynamicrestmapper"
)

func NewController(
	ctx context.Context,
	state *DynamicRESTMapper,
	crdClusterClient *kcpapiextensionsclientset.ClusterClientset,
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
) (*Controller, error) {
	queue := workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.DefaultTypedControllerRateLimiter[string](),
		workqueue.TypedRateLimitingQueueConfig[string]{
			Name: "controllerName",
		},
	)

	c := &Controller{
		queue: queue,

		crdInformer: crdInformer,

		state: state,
	}

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

	return c, nil
}

// Controller watches Shards on the root shard, and then starts informers
// for every Shard, watching the Workspaces on them. It then
// updates the workspace index, which maps logical clusters to shard URLs.
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer

	lock sync.RWMutex

	state *DynamicRESTMapper
}

type gvkrKey struct {
	Gvkr    gvkr
	Key     string
	Deleted bool
}

func encodeGvkrKey(k gvkrKey) (string, error) {
	bs, err := json.Marshal(k)
	return string(bs), err
}

func decodeGvkrKey(k string) (gvkrKey, error) {
	var decoded gvkrKey
	err := json.Unmarshal([]byte(k), &decoded)
	return decoded, err
}

func (c *Controller) enqueueCRD(crd *apiextensionsv1.CustomResourceDefinition, deleted bool) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(crd)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	for _, crdVersion := range crd.Spec.Versions {
		encodedGvkKey, err := encodeGvkrKey(
			gvkrKey{
				Gvkr: gvkr{
					Group:            crd.Spec.Group,
					Version:          crdVersion.Name,
					Kind:             crd.Spec.Names.Kind,
					ResourcePlural:   crd.Spec.Names.Plural,
					ResourceSingular: crd.Spec.Names.Singular,
				},
				Key:     key,
				Deleted: deleted,
			},
		)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("%q controller failed encode CRD object with key %q, err: %w", ControllerName, key, err))
		}
		logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), encodedGvkKey).V(4).Info("queueing CRD")
		c.queue.Add(encodedGvkKey)
	}
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
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
	key := k

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	gvkrKey, err := decodeGvkrKey(key)
	if err != nil {
		return err
	}

	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return nil
	}

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)

	if !gvkrKey.Deleted {
		logger.V(4).Info("adding mapping", "kind", gvkrKey.Gvkr.groupVersionKind())
		c.state.add(clusterName, gvkrKey.Gvkr, meta.RESTScopeRoot)
	} else {
		logger.V(4).Info("removing mapping", "kind", gvkrKey.Gvkr.groupVersionKind())
		c.state.remove(clusterName, gvkrKey.Gvkr.groupVersionKind())
	}

	return nil
}
