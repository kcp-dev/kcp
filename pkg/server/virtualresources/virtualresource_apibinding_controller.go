package virtualresources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	apisv1alpha2informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha2"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
)

const (
	ControllerName = "kcp-virtualresource-apibinding"
)

func objOrTombstone[T runtime.Object](obj any) T {
	if t, ok := obj.(T); ok {
		return t
	}
	if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		if t, ok := tombstone.Obj.(T); ok {
			return t
		}

		panic(fmt.Errorf("tombstone %T is not a %T", tombstone, new(T)))
	}

	panic(fmt.Errorf("%T is not a %T", obj, new(T)))
}

func NewController(
	shardName string,
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	globalShardClusterInformer corev1alpha1informers.ShardClusterInformer,
	localAPIExportInformer, globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	vrServer *Server,
) (*Controller, error) {
	c := &Controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		dynamicClusterClient: dynamicClusterClient,
		server:               vrServer,
		getMyShard: func() (*corev1alpha1.Shard, error) {
			return globalShardClusterInformer.Cluster(core.RootCluster).Lister().Get(shardName)
		},
		getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
			return apiBindingInformer.Cluster(cluster).Lister().Get(name)
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](apisv1alpha2.Resource("apiexports"), localAPIExportInformer.Informer().GetIndexer(), globalAPIExportInformer.Informer().GetIndexer(), path, name)
		},
	}

	logger := logging.WithReconciler(klog.Background(), ControllerName)

	_, _ = apiBindingInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueueAPIBinding(objOrTombstone[*apisv1alpha2.APIBinding](obj), logger) },
		UpdateFunc: func(_, obj interface{}) { c.enqueueAPIBinding(objOrTombstone[*apisv1alpha2.APIBinding](obj), logger) },
		DeleteFunc: func(obj interface{}) { c.enqueueAPIBinding(objOrTombstone[*apisv1alpha2.APIBinding](obj), logger) },
	})

	return c, nil
}

type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	server *Server

	dynamicClusterClient kcpdynamic.ClusterInterface
	getMyShard           func() (*corev1alpha1.Shard, error)
	getAPIBinding        func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error)
	getAPIExport         func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
}

func (c *Controller) enqueueAPIBinding(apiBinding *apisv1alpha2.APIBinding, logger logr.Logger) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(apiBinding)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}

	logging.WithQueueKey(logger, key).V(4).Info(fmt.Sprintf("queueing APIBinding"))
	c.queue.Add(key)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for range numThreads {
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

func (c *Controller) getEndpointSliceURLs(ctx context.Context, cluster logicalcluster.Name, virtualStorage *apisv1alpha2.ResourceSchemaStorageVirtual) ([]string, error) {
	list, err := c.dynamicClusterClient.Cluster(logicalcluster.NewPath(cluster.String())).Resource(schema.GroupVersionResource{
		Group:    virtualStorage.Group,
		Version:  virtualStorage.Version,
		Resource: virtualStorage.Resource,
	}).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var endpointSlice *unstructured.Unstructured
	for i := range list.Items {
		if list.Items[i].GetName() == virtualStorage.Name {
			endpointSlice = &list.Items[i]
			break
		}
	}

	if endpointSlice == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{
			Group:    virtualStorage.Group,
			Resource: virtualStorage.Resource,
		}, virtualStorage.Name)
	}

	endpoints, found, err := unstructured.NestedSlice(endpointSlice.Object, "status", "endpoints")
	if err != nil {
		return nil, fmt.Errorf("failed to get status.endpoints: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("status.endpoints not found")
	}

	var urls []string
	for i, ep := range endpoints {
		endpointMap, ok := ep.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("endpoint at index %d is not an object", i)
		}

		url, found, err := unstructured.NestedString(endpointMap, "url")
		if err != nil {
			return nil, fmt.Errorf("failed to get url from endpoint at index %d: %w", i, err)
		}
		if !found {
			return nil, fmt.Errorf("missing url in endpoint at index %d", i)
		}

		urls = append(urls, url)
	}

	return urls, nil
}

func selectEndpoint(thisShardURL string, urls []string) (string, error) {
	selectedEndpoint := ""
	for _, url := range urls {
		if strings.HasPrefix(url, thisShardURL) {
			if selectedEndpoint == "" {
				selectedEndpoint = url
			} else {
				return "", fmt.Errorf("ambiguous virtual workspace endpoints in endpoint slice: %q and %q for shard %q", selectedEndpoint, url, thisShardURL)
			}
		}
	}
	if selectedEndpoint == "" {
		return "", fmt.Errorf("no suitable virtual workspace endpoint found")
	}

	return selectedEndpoint, nil
}

func (c *Controller) process(ctx context.Context, key string) (bool, error) {
	logger := klog.FromContext(ctx)
	fmt.Println(" >> 0")
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		fmt.Println(" >> 2")
		return false, nil
	}

	binding, err := c.getAPIBinding(clusterName, name)
	var deleted bool
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return true, err
		}
		deleted = true
	}
	if deleted {
		return false, nil
	}

	exportPath := logicalcluster.NewPath(binding.Spec.Reference.Export.Path)
	if exportPath.Empty() {
		exportPath = logicalcluster.From(binding).Path()
	}
	export, err := c.getAPIExport(exportPath, binding.Spec.Reference.Export.Name)
	if err != nil {
		return true, err
	}

	logger = logging.WithObject(logger, binding)
	ctx = klog.NewContext(ctx, logger)

	thisShard, err := c.getMyShard()
	if err != nil {
		return true, err
	}

	for _, resource := range export.Spec.Resources {
		if resource.Storage.Virtual == nil {
			continue
		}

		resourceGR := schema.GroupResource{
			Group:    resource.Group,
			Resource: resource.Name,
		}

		endpointURLs, err := c.getEndpointSliceURLs(ctx, logicalcluster.From(export), resource.Storage.Virtual)
		if err != nil {
			return true, err
		}

		selectedEndpoint, err := selectEndpoint(thisShard.Spec.VirtualWorkspaceURL, endpointURLs)
		if err != nil {
			return true, err
		}

		logger.Info("ADDING HANDLER", "gr", resourceGR, "url", selectedEndpoint)

		if err := c.server.addHandlerFor(clusterName, resourceGR, selectedEndpoint); err != nil {
			return true, err
		}
	}

	return false, nil
}
