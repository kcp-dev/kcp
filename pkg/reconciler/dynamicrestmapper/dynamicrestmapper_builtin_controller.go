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
	"fmt"
	"time"

	"github.com/go-logr/logr"

	apiextensionshelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/tombstone"
	builtinschemas "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
)

const (
	BuiltinTypesControllerName = "kcp-dynamicrestmapper-builtin"
)

var systemCRDClusterName = logicalcluster.Name("system:system-crds")

type BuiltinTypesController struct {
	queue workqueue.TypedRateLimitingInterface[string]

	state         *DynamicRESTMapper
	groupVersions map[string]string

	getCRD func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error)
}

func NewBuiltinTypesController(
	ctx context.Context,
	state *DynamicRESTMapper,
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,
) (*BuiltinTypesController, error) {
	c := &BuiltinTypesController{
		state: state,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: BuiltinTypesControllerName,
			},
		),
		groupVersions: make(map[string]string),
		getCRD: func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			return crdInformer.Lister().Cluster(clusterName).Get(name)
		},
	}

	// Populate the builtin RESTMapper with core types.
	// We assume those never change, so we add mapping for them only during initialization.

	for i := range builtinschemas.BuiltInAPIs {
		group := builtinschemas.BuiltInAPIs[i].GroupVersion.Group
		version := builtinschemas.BuiltInAPIs[i].GroupVersion.Version
		if version > c.groupVersions[group] {
			c.groupVersions[group] = version
		}
		c.state.builtin.add(newTypeMeta(
			builtinschemas.BuiltInAPIs[i].GroupVersion.Group,
			builtinschemas.BuiltInAPIs[i].GroupVersion.Version,
			builtinschemas.BuiltInAPIs[i].Names.Kind,
			builtinschemas.BuiltInAPIs[i].Names.Singular,
			builtinschemas.BuiltInAPIs[i].Names.Plural,
			resourceScopeToRESTScope(builtinschemas.BuiltInAPIs[i].ResourceScope)),
		)
	}

	logger := logging.WithReconciler(klog.Background(), BuiltinTypesControllerName)

	// System CRDs could change over time, and so we build that part of the mapper dynamically.

	// We are only interested in system CRDs.
	_, _ = crdInformer.Informer().Cluster("system:system-crds").AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueCRD(tombstone.Obj[*apiextensionsv1.CustomResourceDefinition](obj), logger)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.enqueueCRD(tombstone.Obj[*apiextensionsv1.CustomResourceDefinition](newObj), logger)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueCRD(tombstone.Obj[*apiextensionsv1.CustomResourceDefinition](obj), logger)
		},
	})

	return c, nil
}

func (c *BuiltinTypesController) enqueueCRD(crd *apiextensionsv1.CustomResourceDefinition, logger logr.Logger) {
	if !apiextensionshelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established) {
		// The CRD is not ready yet. Nothing to do, we'll get notified on the next update event.
		return
	}

	// CRD name is enforced to be in format "<Resource>.<Group>", e.g. "cowboys.wildwest.dev".
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(crd)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logger.V(4).Info("queueing system CRD")
	c.queue.Add(key)
}

func (c *BuiltinTypesController) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), BuiltinTypesControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for range numThreads {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *BuiltinTypesController) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *BuiltinTypesController) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	key, quit := c.queue.Get()
	if quit {
		return false
	}

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", DynamicTypesControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *BuiltinTypesController) gatherGVKRsForCRD(crd *apiextensionsv1.CustomResourceDefinition) []typeMeta {
	if crd == nil {
		return nil
	}
	gvkrs := make([]typeMeta, 0, len(crd.Spec.Versions))
	for _, version := range crd.Spec.Versions {
		if !version.Served {
			continue
		}

		gvkrs = append(gvkrs, newTypeMeta(
			crd.Spec.Group,
			version.Name,
			crd.Status.AcceptedNames.Kind,
			crd.Status.AcceptedNames.Singular,
			crd.Status.AcceptedNames.Plural,
			resourceScopeToRESTScope(crd.Spec.Scope),
		))
	}
	return gvkrs
}

func (c *BuiltinTypesController) gatherGVKRsForMappedGroupResource(gr schema.GroupResource) ([]typeMeta, error) {
	c.state.lock.RLock()
	defer c.state.lock.RUnlock()

	gvkrs, err := c.state.builtin.getGVKRs(gr)
	if err != nil {
		if meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, err
	}
	return gvkrs, nil
}

func (c *BuiltinTypesController) process(ctx context.Context, key string) error {
	_, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return err
	}

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)

	// CRD name is enforced to be in format "<Resource>.<Group>", e.g. "cowboys.wildwest.dev".
	gr := schema.ParseGroupResource(name)

	crd, err := c.getCRD(systemCRDClusterName, name)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	// Remove and add the mapping -- this way we can refresh any existing mappings.
	typeMetaToRemove, err := c.gatherGVKRsForMappedGroupResource(gr)
	if err != nil {
		return err
	}
	typeMetaToAdd := c.gatherGVKRsForCRD(crd)

	logger.V(4).Info("applying mappings")

	c.state.lock.Lock()
	defer c.state.lock.Unlock()
	c.state.builtin.apply(typeMetaToRemove, typeMetaToAdd)
	return nil
}
