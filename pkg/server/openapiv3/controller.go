/*
Copyright 2024 The KCP Authors.

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

package openapiv3

import (
	"context"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	kcpapiextensionsv1listers "github.com/kcp-dev/client-go/apiextensions/listers/apiextensions/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	apiextensionshelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/controller/openapi/builder"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/cached"
	"k8s.io/kube-openapi/pkg/spec3"

	"github.com/kcp-dev/kcp/pkg/logging"
)

const ControllerName = "kcp-openapiv3"
const CrdControllerName = "crd_openapi_v3_controller"

type CRDSpecGetter interface {
	GetCRDSpecs(clusterName logicalcluster.Name, name string) (specs map[string]cached.Value[*spec3.OpenAPI], err error)
}

// Controller watches CustomResourceDefinitions and publishes OpenAPI v3.
type Controller struct {
	crdLister  kcpapiextensionsv1listers.CustomResourceDefinitionClusterLister
	crdsSynced cache.InformerSynced

	queue workqueue.TypedRateLimitingInterface[string]

	// specs per version, logical cluster and per CRD name
	lock                 sync.Mutex
	byClusterNameVersion map[logicalcluster.Name]map[string]map[string]cached.Value[*spec3.OpenAPI]
}

// NewController creates a new Controller with input CustomResourceDefinition informer.
func NewController(crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer) *Controller {
	c := &Controller{
		crdLister:  crdInformer.Lister(),
		crdsSynced: crdInformer.Informer().HasSynced,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: CrdControllerName,
			},
		),
		byClusterNameVersion: map[logicalcluster.Name]map[string]map[string]cached.Value[*spec3.OpenAPI]{},
	}

	crdInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{ //nolint:errcheck
		AddFunc:    c.addCustomResourceDefinition,
		UpdateFunc: c.updateCustomResourceDefinition,
		DeleteFunc: c.deleteCustomResourceDefinition,
	})

	return c
}

func (c *Controller) Run(ctx context.Context) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	log := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, log)
	log.Info("Starting controller")
	defer log.Info("Shutting down controller")

	if !cache.WaitForCacheSync(ctx.Done(), c.crdsSynced) {
		utilruntime.HandleError(fmt.Errorf("timed out waiting for caches to sync"))
		return
	}

	crds, err := c.crdLister.List(labels.Everything())
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("failed to initially list all CRDs: %v", err))
		return
	}
	for _, crd := range crds {
		c.processCRD(crd)
	}

	go wait.Until(func() { c.startWorker(ctx) }, time.Second, ctx.Done())

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

	log := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, log)
	log.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	// log slow aggregations
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		if elapsed > time.Second {
			log.Info("slow openapi v3 aggregation", "duration", elapsed)
		}
	}()

	if requeue, err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	} else if requeue {
		// only requeue if we didn't error, but we still want to requeue
		c.queue.Add(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) (bool, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return false, nil
	}
	crd, err := c.crdLister.Cluster(clusterName).Get(name)
	if err != nil && !errors.IsNotFound(err) {
		return false, err
	}

	if errors.IsNotFound(err) || !apiextensionshelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established) {
		delete(c.byClusterNameVersion[clusterName], name)
		if len(c.byClusterNameVersion[clusterName]) == 0 {
			delete(c.byClusterNameVersion, clusterName)
		}
		return false, nil
	}

	c.processCRD(crd)

	return false, nil
}

func (c *Controller) processCRD(crd *apiextensionsv1.CustomResourceDefinition) {
	clusterName := logicalcluster.From(crd)

	// remove old instance
	delete(c.byClusterNameVersion[clusterName], crd.Name)

	// add new instance with all versions
	for _, v := range crd.Spec.Versions {
		if !v.Served {
			delete(c.byClusterNameVersion[clusterName][crd.Name], v.Name)
			continue
		}

		spec := cached.Once(cached.Func[*spec3.OpenAPI](
			func() (value *spec3.OpenAPI, etag string, err error) {
				spec, err := builder.BuildOpenAPIV3(crd, v.Name, builder.Options{V2: false})
				if err != nil {
					return nil, "", err
				}
				bs, err := json.Marshal(spec)
				if err != nil {
					return nil, "", err
				}
				return spec, fmt.Sprintf("%X", sha512.Sum512(bs)), nil
			},
		))

		if c.byClusterNameVersion[clusterName] == nil {
			c.byClusterNameVersion[clusterName] = map[string]map[string]cached.Value[*spec3.OpenAPI]{}
		}
		if c.byClusterNameVersion[clusterName][crd.Name] == nil {
			c.byClusterNameVersion[clusterName][crd.Name] = map[string]cached.Value[*spec3.OpenAPI]{}
		}
		c.byClusterNameVersion[clusterName][crd.Name][v.Name] = spec
	}
}

func (c *Controller) addCustomResourceDefinition(obj interface{}) {
	castObj := obj.(*apiextensionsv1.CustomResourceDefinition)
	c.enqueue(castObj)
}

func (c *Controller) updateCustomResourceDefinition(oldObj, newObj interface{}) {
	castNewObj := newObj.(*apiextensionsv1.CustomResourceDefinition)
	c.enqueue(castNewObj)
}

func (c *Controller) deleteCustomResourceDefinition(obj interface{}) {
	castObj, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			return
		}
		castObj, ok = tombstone.Obj.(*apiextensionsv1.CustomResourceDefinition)
		if !ok {
			return
		}
	}
	c.enqueue(castObj)
}

func (c *Controller) enqueue(obj *apiextensionsv1.CustomResourceDefinition) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", obj, err))
		return
	}
	c.queue.Add(key)
}

func (c *Controller) GetCRDSpecs(clusterName logicalcluster.Name, name string) (specs map[string]cached.Value[*spec3.OpenAPI], err error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	specs, ok := c.byClusterNameVersion[clusterName][name]
	if !ok {
		return nil, fmt.Errorf("CRD %s|%s not found", clusterName, name)
	}

	return specs, nil
}
