package syncer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kcp-dev/kcp/pkg/util/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog"
)

const resyncPeriod = 10 * time.Hour

type UpsertFunc func(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured) error
type DeleteFunc func(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error
type HandlersProvider func(c *Controller, gvr schema.GroupVersionResource) cache.ResourceEventHandlerFuncs

type Controller struct {
	queue workqueue.RateLimitingInterface

	// Upstream
	fromDSIF dynamicinformer.DynamicSharedInformerFactory

	// Downstream
	toClient dynamic.Interface

	stopCh chan struct{}

	upsertFn UpsertFunc
	deleteFn DeleteFunc
}

// New returns a new syncer Controller syncing spec from "from" to "to".
func New(from, to *rest.Config, upsertFn UpsertFunc, deleteFn DeleteFunc, handlers HandlersProvider, syncedResourceTypes []string, clusterID string) (*Controller, error) {
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	stopCh := make(chan struct{})

	c := Controller{
		// TODO: should we have separate upstream and downstream sync workqueues?
		queue: queue,

		toClient: dynamic.NewForConfigOrDie(to),

		stopCh: stopCh,

		upsertFn: upsertFn,
		deleteFn: deleteFn,
	}

	fromClient := dynamic.NewForConfigOrDie(from)
	fromDSIF := dynamicinformer.NewFilteredDynamicSharedInformerFactory(fromClient, resyncPeriod, metav1.NamespaceAll, func(o *metav1.ListOptions) {
		o.LabelSelector = fmt.Sprintf("kcp.dev/cluster=%s", clusterID)
	})

	// Get all types the upstream API server knows about.
	// TODO: watch this and learn about new types, or forget about old ones.
	gvrstrs, err := getAllGVRs(from, syncedResourceTypes...)
	if err != nil {
		return nil, err
	}
	for _, gvrstr := range gvrstrs {
		gvr, _ := schema.ParseResourceArg(gvrstr)

		if _, err := fromDSIF.ForResource(*gvr).Lister().List(labels.Everything()); err != nil {
			klog.Infof("Failed to list all %q: %v", gvrstr, err)
			return nil, errors.NewRetryableError(err)
		}

		fromDSIF.ForResource(*gvr).Informer().AddEventHandler(handlers(&c, *gvr))
		klog.Infof("Set up informer for %v", gvr)
	}
	fromDSIF.WaitForCacheSync(stopCh)
	fromDSIF.Start(stopCh)
	c.fromDSIF = fromDSIF

	return &c, nil
}

func contains(ss []string, s string) bool {
	for _, n := range ss {
		if n == s {
			return true
		}
	}
	return false
}

func getAllGVRs(config *rest.Config, resourcesToSync ...string) ([]string, error) {
	toSyncSet := sets.NewString(resourcesToSync...)
	willBeSyncedSet := sets.NewString()
	dc, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}
	rs, err := dc.ServerPreferredResources()
	if err != nil {
		if strings.Contains(err.Error(), "unable to retrieve the complete list of server APIs") {
			// This error may occur when some API resources added from CRDs are not completely ready.
			// We should just retry without a limit on the number of retries in such a case.
			//
			// In fact this might be related to a bug in the changes made on the feature-logical-cluster
			// Kubernetes branch to support legacy schema resources added as CRDs.
			// If this is confirmed, this test will be removed when the CRD bug is fixed.
			return nil, errors.NewRetryableError(err)
		} else {
			return nil, err
		}
	}
	var gvrstrs []string
	for _, r := range rs {
		// v1 -> v1.
		// apps/v1 -> v1.apps
		// tekton.dev/v1beta1 -> v1beta1.tekton.dev
		parts := strings.SplitN(r.GroupVersion, "/", 2)
		vr := parts[0] + "."
		if len(parts) == 2 {
			vr = parts[1] + "." + parts[0]
		}
		for _, ai := range r.APIResources {
			if !toSyncSet.Has(ai.Name) {
				// We're not interested in this resource type
				continue
			}
			if strings.Contains(ai.Name, "/") {
				// foo/status, pods/exec, namespace/finalize, etc.
				continue
			}
			if !ai.Namespaced {
				// Ignore cluster-scoped things.
				continue
			}
			if !contains(ai.Verbs, "watch") {
				klog.Infof("resource %s %s is not watchable: %v", vr, ai.Name, ai.Verbs)
				continue
			}
			gvrstrs = append(gvrstrs, fmt.Sprintf("%s.%s", ai.Name, vr))
			willBeSyncedSet.Insert(ai.Name)
		}
	}

	notFoundResourceTypes := toSyncSet.Difference(willBeSyncedSet)
	if notFoundResourceTypes.Len() != 0 {
		// Some of the API resources expected to be there are still not published by KCP.
		// We should just retry without a limit on the number of retries in such a case,
		// until the corresponding resources are added inside KCP as CRDs and published as API resources.
		return nil, errors.NewRetryableError(fmt.Errorf("The following resource types were requested to be synced, but were not found in the KCP logical cluster: %v", notFoundResourceTypes.List()))
	}
	return gvrstrs, nil
}

type holder struct {
	gvr schema.GroupVersionResource
	obj interface{}
}

func (c *Controller) AddToQueue(gvr schema.GroupVersionResource, obj interface{}) {
	c.queue.AddRateLimited(holder{gvr: gvr, obj: obj})
}

// Start starts N worker processes processing work items.
func (c *Controller) Start(numThreads int) {
	for i := 0; i < numThreads; i++ {
		go c.startWorker()
	}
}

// startWorker processes work items until stopCh is closed.
func (c *Controller) startWorker() {
	for {
		select {
		case <-c.stopCh:
			klog.Info("stopping syncer worker")
			return
		default:
			c.processNextWorkItem()
		}
	}
}

// Stop stops the syncer.
func (c *Controller) Stop() {
	c.queue.ShutDown()
	close(c.stopCh)
}

// Done returns a channel that's closed when the syncer is stopped.
func (c *Controller) Done() <-chan struct{} { return c.stopCh }

func (c *Controller) processNextWorkItem() bool {
	// Wait until there is a new item in the working queue
	i, quit := c.queue.Get()
	if quit {
		return false
	}
	h := i.(holder)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(i)

	err := c.process(h.gvr, h.obj)
	c.handleErr(err, i)
	return true
}

func (c *Controller) handleErr(err error, i interface{}) {
	// Reconcile worked, nothing else to do for this workqueue item.
	if err == nil {
		c.queue.Forget(i)
		return
	}

	// Re-enqueue up to 5 times.
	num := c.queue.NumRequeues(i)
	if num < 5 {
		klog.Errorf("Error reconciling key %q, retrying... (#%d): %v", i, num, err)
		c.queue.AddRateLimited(i)
		return
	}

	// Give up and report error elsewhere.
	c.queue.Forget(i)
	utilruntime.HandleError(err)
	klog.Errorf("Dropping key %q after failed retries: %v", i, err)
}

func (c *Controller) process(gvr schema.GroupVersionResource, obj interface{}) error {
	klog.V(2).Infof("Process object of type: %T : %v", obj, obj)
	meta, isMeta := obj.(metav1.Object)
	if !isMeta {
		if tombstone, isTombstone := obj.(cache.DeletedFinalStateUnknown); isTombstone {
			meta, isMeta = tombstone.Obj.(metav1.Object)
			if !isMeta {
				err := fmt.Errorf("Tombstone contained object is expected to be a metav1.Object, but is %T: %#v", obj, obj)
				klog.Error(err)
				return err
			}
		} else {
			err := fmt.Errorf("Object to synchronize is expected to be a metav1.Object, but is %T", obj)
			klog.Error(err)
			return err
		}
	}
	namespace, name := meta.GetNamespace(), meta.GetName()

	ctx := context.TODO()

	obj, exists, err := c.fromDSIF.ForResource(gvr).Informer().GetIndexer().Get(obj)
	if err != nil {
		klog.Error(err)
		return err
	}

	if !exists {
		klog.Infof("Object with gvr=%q was deleted : %s/%s", gvr, namespace, name)
		c.deleteFn(c, ctx, gvr, namespace, name)
		return nil
	}

	unstrob, isUnstructured := obj.(*unstructured.Unstructured)
	if !isUnstructured {
		err := fmt.Errorf("Object to synchronize is expected to be Unstructured, but is %T", obj)
		klog.Error(err)
		return err
	}

	if c.upsertFn != nil {
		return c.upsertFn(c, ctx, gvr, namespace, unstrob)
	}

	return err
}

// getClient gets a dynamic client for the GVR, scoped to namespace if the namespace is not "".
func (c *Controller) getClient(gvr schema.GroupVersionResource, namespace string) dynamic.ResourceInterface {
	nri := c.toClient.Resource(gvr)
	if namespace != "" {
		return nri.Namespace(namespace)
	}
	return nri
}
