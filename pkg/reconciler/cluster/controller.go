package cluster

import (
	"context"
	"log"
	"time"

	"github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	clusterclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/cluster/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

const resyncPeriod = 10 * time.Hour

// NewController returns a new Controller which reconciles Cluster resources in the API
// server it reaches using the REST client.
//
// When new Clusters are found, the syncer will be run there using the given image.
func NewController(cfg *rest.Config, syncerImage, kubeconfig string) *Controller {
	client := clusterv1alpha1.NewForConfigOrDie(cfg)
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	stopCh := make(chan struct{}) // TODO: hook this up to SIGTERM/SIGINT

	crdClient := apiextensionsv1client.NewForConfigOrDie(cfg)

	c := &Controller{
		queue:       queue,
		client:      client,
		crdClient:   crdClient,
		syncerImage: syncerImage,
		kubeconfig:  kubeconfig,
		stopCh:      stopCh,
	}

	sif := externalversions.NewSharedInformerFactoryWithOptions(clusterclient.NewForConfigOrDie(cfg), resyncPeriod)
	sif.Cluster().V1alpha1().Clusters().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})
	c.indexer = sif.Cluster().V1alpha1().Clusters().Informer().GetIndexer()
	sif.WaitForCacheSync(stopCh)
	sif.Start(stopCh)

	return c
}

type Controller struct {
	queue       workqueue.RateLimitingInterface
	client      clusterv1alpha1.ClusterV1alpha1Interface
	indexer     cache.Indexer
	crdClient   apiextensionsv1client.ApiextensionsV1Interface
	syncerImage string
	kubeconfig  string
	stopCh      chan struct{}
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	c.queue.Add(key)
}

func (c *Controller) Start(numThreads int) {
	defer c.queue.ShutDown()
	for i := 0; i < numThreads; i++ {
		go wait.Until(c.startWorker, time.Second, c.stopCh)
	}
	log.Println("Starting workers")
	<-c.stopCh
	log.Println("Stopping workers")
}

func (c *Controller) startWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *Controller) processNextWorkItem() bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	err := c.process(key)
	c.handleErr(err, key)
	return true
}

func (c *Controller) handleErr(err error, key string) {
	// Reconcile worked, nothing else to do for this workqueue item.
	if err == nil {
		log.Println("Successfully reconciled", key)
		c.queue.Forget(key)
		return
	}

	// Re-enqueue up to 5 times.
	num := c.queue.NumRequeues(key)
	if num < 5 {
		log.Printf("Error reconciling key %q, retrying... (#%d): %v", key, num, err)
		c.queue.AddRateLimited(key)
		return
	}

	// Give up and report error elsewhere.
	c.queue.Forget(key)
	runtime.HandleError(err)
	log.Printf("Dropping key %q after failed retries: %v", key, err)
}

func (c *Controller) process(key string) error {
	obj, exists, err := c.indexer.GetByKey(key)
	if err != nil {
		return err
	}

	if !exists {
		log.Printf("Object with key %q was deleted", key)
		return nil
	}
	current := obj.(*v1alpha1.Cluster)
	previous := current.DeepCopy()

	ctx := context.TODO()

	if err := c.reconcile(ctx, current); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(previous.Status, current.Status) {
		log.Println("saw update")
		_, uerr := c.client.Clusters().UpdateStatus(ctx, current, metav1.UpdateOptions{})
		return uerr
	}
	log.Println("no update")

	return nil
}
