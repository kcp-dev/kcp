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

package cachedresources

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpapiextensionsv1informers "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpcorev1informers "github.com/kcp-dev/client-go/informers/core/v1"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	replicationcontroller "github.com/kcp-dev/kcp/pkg/reconciler/cache/cachedresources/replication"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	"github.com/kcp-dev/kcp/pkg/reconciler/dynamicrestmapper"
	"github.com/kcp-dev/kcp/pkg/tombstone"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	cachev1alpha1client "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/typed/cache/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
	apisv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha2"
	cacheinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/cache/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
	apisv1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/apis/v1alpha1"
	cachev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/cache/v1alpha1"
)

// This Controller starts 1 published resource controller per shard.
// This is bit wasteful for now. We can optimize this by starting a single controller
// per logical cluster. But this required stopping and starting the controller when a
// new published resource is created. This would be fist optimization we need todo as follow-up
// before removing feature flag and making it enabled by default.
const (
	ControllerName = "kcp-cached-resources-controller"

	DefaultIdentitySecretNamespace = "kcp-system"
)

// NewController returns a new controller for CachedResource objects.
func NewController(
	shardName string,
	kcpClusterClient kcpclientset.ClusterInterface,
	kcpCacheClient kcpclientset.ClusterInterface,
	crdClusterClient kcpapiextensionsclientset.ClusterInterface,
	dynamicClient kcpdynamic.ClusterInterface,
	cacheDynamicClient kcpdynamic.ClusterInterface,

	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	namespaceInformer kcpcorev1informers.NamespaceClusterInformer,
	secretInformer kcpcorev1informers.SecretClusterInformer,
	crdInformer kcpapiextensionsv1informers.CustomResourceDefinitionClusterInformer,

	dynRESTMapper *dynamicrestmapper.DynamicRESTMapper,

	discoveringDynamicKcpInformers *informer.DiscoveringDynamicSharedInformerFactory,
	cacheKcpInformers kcpinformers.SharedInformerFactory,

	cachedResourceInformer cacheinformers.CachedResourceClusterInformer,
	cachedResourceEndpointSliceInformer cacheinformers.CachedResourceEndpointSliceClusterInformer,

	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,

	apiExportInformer apisv1alpha2informers.APIExportClusterInformer,
	globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,

	apiResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
	globalAPIResourceSchemaInformer apisv1alpha1informers.APIResourceSchemaClusterInformer,
) (*Controller, error) {
	c := &Controller{
		shardName: shardName,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		kcpClient:      kcpClusterClient,
		kcpCacheClient: kcpCacheClient,

		dynamicClient:      dynamicClient,
		cacheDynamicClient: cacheDynamicClient,

		dynRESTMapper: dynRESTMapper,

		discoveringDynamicKcpInformers: discoveringDynamicKcpInformers,
		cacheKcpInformers:              cacheKcpInformers,

		CachedResourceLister:  cachedResourceInformer.Lister(),
		CachedResourceIndexer: cachedResourceInformer.Informer().GetIndexer(),

		CachedResourceEndpointSliceInformer: cachedResourceEndpointSliceInformer,

		commit: committer.NewCommitter[*cachev1alpha1.CachedResource, cachev1alpha1client.CachedResourceInterface, *cachev1alpha1.CachedResourceSpec, *cachev1alpha1.CachedResourceStatus](kcpClusterClient.CacheV1alpha1().CachedResources()),

		secretNamespace: DefaultIdentitySecretNamespace,

		getNamespace: func(clusterName logicalcluster.Name, name string) (*corev1.Namespace, error) {
			return namespaceInformer.Lister().Cluster(clusterName).Get(name)
		},
		createNamespace: func(ctx context.Context, clusterName logicalcluster.Path, ns *corev1.Namespace) error {
			_, err := kubeClusterClient.Cluster(clusterName).CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
			return err
		},
		getSecret: func(ctx context.Context, clusterName logicalcluster.Name, ns, name string) (*corev1.Secret, error) {
			secret, err := secretInformer.Lister().Cluster(clusterName).Secrets(ns).Get(name)
			if err == nil {
				return secret, nil
			}

			// In case the lister is slow to catch up, try a live read
			secret, err = kubeClusterClient.Cluster(clusterName.Path()).CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}

			return secret, nil
		},
		createSecret: func(ctx context.Context, clusterName logicalcluster.Path, secret *corev1.Secret) error {
			_, err := kubeClusterClient.Cluster(clusterName).CoreV1().Secrets(secret.Namespace).Create(ctx, secret, metav1.CreateOptions{})
			return err
		},
		getEndpointSlice: func(ctx context.Context, clusterName logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error) {
			return cachedResourceEndpointSliceInformer.Lister().Cluster(clusterName).Get(name)
		},
		createEndpointSlice: func(ctx context.Context, cluster logicalcluster.Path, endpointSlice *cachev1alpha1.CachedResourceEndpointSlice) error {
			_, err := kcpClusterClient.CacheV1alpha1().CachedResourceEndpointSlices().Cluster(cluster).
				Create(ctx, endpointSlice, metav1.CreateOptions{})
			return err
		},

		getLogicalCluster: func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return logicalClusterInformer.Cluster(cluster).Lister().Get(corev1alpha1.LogicalClusterName)
		},
		getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
			return apiBindingInformer.Cluster(cluster).Lister().Get(name)
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](apisv1alpha2.Resource("apiexports"), apiExportInformer.Informer().GetIndexer(), globalAPIExportInformer.Informer().GetIndexer(), path, name)
		},
		getAPIResourceSchema: informer.NewScopedGetterWithFallback[*apisv1alpha1.APIResourceSchema, apisv1alpha1listers.APIResourceSchemaLister](apiResourceSchemaInformer.Lister(), globalAPIResourceSchemaInformer.Lister()),
		getLocalAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			return apiResourceSchemaInformer.Cluster(cluster).Lister().Get(name)
		},

		listCRDsByGR: func(cluster logicalcluster.Name, gr schema.GroupResource) ([]*apiextensionsv1.CustomResourceDefinition, error) {
			crds, err := crdInformer.Cluster(cluster).Lister().List(labels.Everything())
			if err != nil {
				return nil, err
			}

			var crdsWithGR []*apiextensionsv1.CustomResourceDefinition
			for _, crd := range crds {
				if crd.Spec.Group == gr.Group && crd.Spec.Names.Plural == gr.Resource {
					crdsWithGR = append(crdsWithGR, crd)
				}
			}
			return crdsWithGR, nil
		},

		getCRD: func(ctx context.Context, cluster logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			crd, err := crdInformer.Lister().Cluster(cluster).Get(name)
			if err == nil {
				return crd, nil
			}

			// In case the lister is slow to catch up, try a live read
			crd, err = crdClusterClient.Cluster(cluster.Path()).ApiextensionsV1().CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}

			return crd, nil
		},

		createCachedAPIResourceSchema: func(ctx context.Context, cluster logicalcluster.Name, sch *apisv1alpha1.APIResourceSchema) error {
			_, err := kcpClusterClient.Cluster(cluster.Path()).ApisV1alpha1().APIResourceSchemas().Create(ctx, sch, metav1.CreateOptions{})
			return err
		},

		updateCreateAPIResourceSchema: func(ctx context.Context, cluster logicalcluster.Name, sch *apisv1alpha1.APIResourceSchema) error {
			_, err := kcpClusterClient.Cluster(cluster.Path()).ApisV1alpha1().APIResourceSchemas().Update(ctx, sch, metav1.UpdateOptions{})
			return err
		},

		controllerRegistry: newRegistry(),
	}

	_, _ = cachedResourceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) { c.enqueueCachedResource(tombstone.Obj[*cachev1alpha1.CachedResource](obj), "") },
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueCachedResource(tombstone.Obj[*cachev1alpha1.CachedResource](obj), "")
		},
		DeleteFunc: func(obj interface{}) { c.enqueueCachedResource(tombstone.Obj[*cachev1alpha1.CachedResource](obj), "") },
	})

	_, _ = crdInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueCachedResourcesInCluster(obj.(metav1.Object), " because of CRD update")
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueCachedResourcesInCluster(obj.(metav1.Object), " because of CRD update")
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueCachedResourcesInCluster(obj.(metav1.Object), " because of CRD update")
		},
	})

	_, _ = apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.enqueueCachedResourcesInCluster(obj.(metav1.Object), " because of APIBinding update")
		},
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueCachedResourcesInCluster(obj.(metav1.Object), " because of APIBinding update")
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueCachedResourcesInCluster(obj.(metav1.Object), " because of APIBinding update")
		},
	})

	return c, nil
}

type CachedResourceResource = committer.Resource[*cachev1alpha1.CachedResourceSpec, *cachev1alpha1.CachedResourceStatus]

type Controller struct {
	shardName string
	queue     workqueue.TypedRateLimitingInterface[string]

	kcpClient      kcpclientset.ClusterInterface
	kcpCacheClient kcpclientset.ClusterInterface

	dynamicClient      kcpdynamic.ClusterInterface
	cacheDynamicClient kcpdynamic.ClusterInterface

	dynRESTMapper *dynamicrestmapper.DynamicRESTMapper

	discoveringDynamicKcpInformers *informer.DiscoveringDynamicSharedInformerFactory
	cacheKcpInformers              kcpinformers.SharedInformerFactory

	CachedResourceIndexer cache.Indexer
	CachedResourceLister  cachev1alpha1listers.CachedResourceClusterLister

	CachedResourceEndpointSliceInformer cacheinformers.CachedResourceEndpointSliceClusterInformer

	commit func(ctx context.Context, new, old *CachedResourceResource) error

	secretNamespace string

	getNamespace    func(clusterName logicalcluster.Name, name string) (*corev1.Namespace, error)
	createNamespace func(ctx context.Context, clusterName logicalcluster.Path, ns *corev1.Namespace) error

	getSecret    func(ctx context.Context, clusterName logicalcluster.Name, ns, name string) (*corev1.Secret, error)
	createSecret func(ctx context.Context, clusterName logicalcluster.Path, secret *corev1.Secret) error

	getEndpointSlice    func(ctx context.Context, clusterName logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error)
	createEndpointSlice func(ctx context.Context, clusterName logicalcluster.Path, endpointSlice *cachev1alpha1.CachedResourceEndpointSlice) error

	getLogicalCluster         func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)
	getAPIBinding             func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error)
	getAPIExport              func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getAPIResourceSchema      func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	getLocalAPIResourceSchema func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error)
	listCRDsByGR              func(cluster logicalcluster.Name, gr schema.GroupResource) ([]*apiextensionsv1.CustomResourceDefinition, error)

	getCRD func(ctx context.Context, cluster logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error)

	createCachedAPIResourceSchema func(ctx context.Context, cluster logicalcluster.Name, sch *apisv1alpha1.APIResourceSchema) error
	updateCreateAPIResourceSchema func(ctx context.Context, cluster logicalcluster.Name, sch *apisv1alpha1.APIResourceSchema) error

	controllerRegistry *controllerRegistry

	started bool
}

func (c *Controller) enqueueCachedResourcesInCluster(metaObj metav1.Object, logSuffix string) {
	cachedResources, err := c.CachedResourceLister.Cluster(logicalcluster.From(metaObj)).List(labels.Everything())
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	for _, cr := range cachedResources {
		c.enqueueCachedResource(cr, logSuffix)
	}
}

func (c *Controller) enqueueCachedResource(cachedResource *cachev1alpha1.CachedResource, logSuffix string) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(cachedResource)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info(fmt.Sprintf("queueing CachedResource%s", logSuffix))
	c.queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(cacheclient.WithShardInContext(ctx, shard.New(c.shardName)), logger)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for range numThreads {
		go wait.Until(func() { c.startWorker(ctx) }, time.Second, ctx.Done())
	}
	c.started = true
	<-ctx.Done()
	c.started = false
}

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

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if requeue, err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	} else if requeue {
		c.queue.Add(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) (bool, error) {
	parent, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		return false, err
	}

	CachedResource, err := c.CachedResourceLister.Cluster(parent).Get(name)
	if err != nil {
		if kerrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	old := CachedResource
	CachedResource = CachedResource.DeepCopy()

	logger := logging.WithObject(klog.FromContext(ctx), CachedResource)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	requeue, err := c.reconcile(ctx, parent, CachedResource)
	if err != nil {
		errs = append(errs, err)
	}

	oldResource := &CachedResourceResource{ObjectMeta: old.ObjectMeta, Spec: &old.Spec, Status: &old.Status}
	newResource := &CachedResourceResource{ObjectMeta: CachedResource.ObjectMeta, Spec: &CachedResource.Spec, Status: &CachedResource.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	return requeue, utilerrors.NewAggregate(errs)
}

func newRegistry() *controllerRegistry {
	return &controllerRegistry{
		controllers: make(map[string]*replicationcontroller.Controller),
		cancels:     make(map[string]context.CancelFunc),
	}
}

type controllerRegistry struct {
	mu          sync.RWMutex
	controllers map[string]*replicationcontroller.Controller
	cancels     map[string]context.CancelFunc
}

func (c *controllerRegistry) register(name string, controller *replicationcontroller.Controller, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.controllers[name] = controller
	c.cancels[name] = cancel
}

func (c *controllerRegistry) get(name string) *replicationcontroller.Controller {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.controllers[name]
}

func (c *controllerRegistry) unregister(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.controllers[name]; ok {
		if c.cancels[name] != nil {
			c.cancels[name]()
		}
	}
	delete(c.controllers, name)
	delete(c.cancels, name)
}
