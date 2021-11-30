package cluster

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/api/genericcontrolplanescheme"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	apiresourceinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apiresource/v1alpha1"
	clusterinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/cluster/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/apiresource"
	"github.com/kcp-dev/kcp/pkg/syncer"
)

type SyncerMode int

const (
	SyncerModePull SyncerMode = iota
	SyncerModePush
	SyncerModeNone

	controllerName = "cluster"
)

const GVRForLocationInLogicalClusterIndexName = "GVRForLocationInLogicalCluster"

func GetGVRForLocationInLogicalClusterIndexKey(location, clusterName string, gvr metav1.GroupVersionResource) string {
	return location + "$$" + apiresource.GetClusterNameAndGVRIndexKey(clusterName, gvr)
}

const LocationInLogicalClusterIndexName = "LocationInLogicalCluster"

func GetLocationInLogicalClusterIndexKey(location, clusterName string) string {
	return location + "/" + clusterName
}

// NewController returns a new Controller which reconciles Cluster resources in the API
// server it reaches using the REST client.
//
// When new Clusters are found, the syncer will be run there using the given image.
func NewController(
	apiExtensionsClient apiextensionsclient.Interface,
	kcpClient kcpclient.Interface,
	clusterInformer clusterinformer.ClusterInformer,
	apiResourceImportInformer apiresourceinformer.APIResourceImportInformer,
	syncerImage string,
	kubeconfig clientcmdapi.Config,
	resourcesToSync []string,
	syncerMode SyncerMode,
) (*Controller, error) {
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	var genericControlPlaneResources []schema.GroupVersionResource

	//TODO(ncdc): does this need a per-cluster client?
	discoveryClient := apiExtensionsClient.Discovery()
	serverGroups, err := discoveryClient.ServerGroups()
	if err != nil {
		return nil, err
	}
	for _, apiGroup := range serverGroups.Groups {
		if genericcontrolplanescheme.Scheme.IsGroupRegistered(apiGroup.Name) {
			for _, version := range apiGroup.Versions {
				gv := schema.GroupVersion{
					Group:   apiGroup.Name,
					Version: version.Version,
				}
				if genericcontrolplanescheme.Scheme.IsVersionRegistered(gv) {
					apiResourceList, err := discoveryClient.ServerResourcesForGroupVersion(gv.String())
					if err != nil {
						return nil, err
					}
					for _, apiResource := range apiResourceList.APIResources {
						gvk := gv.WithKind(apiResource.Kind)
						if !strings.Contains(apiResource.Name, "/") && genericcontrolplanescheme.Scheme.Recognizes(gvk) {
							genericControlPlaneResources = append(genericControlPlaneResources, gv.WithResource(apiResource.Name))
						}
					}
				}
			}
		}
	}

	c := &Controller{
		queue:                    queue,
		apiExtensionsClient:      apiExtensionsClient,
		kcpClient:                kcpClient,
		clusterIndexer:           clusterInformer.Informer().GetIndexer(),
		apiresourceImportIndexer: apiResourceImportInformer.Informer().GetIndexer(),
		syncChecks: []cache.InformerSynced{
			clusterInformer.Informer().HasSynced,
			apiResourceImportInformer.Informer().HasSynced,
		},
		syncerImage:                  syncerImage,
		kubeconfig:                   kubeconfig,
		resourcesToSync:              resourcesToSync,
		syncerMode:                   syncerMode,
		syncers:                      map[string]*syncer.Syncer{},
		apiImporters:                 map[string]*APIImporter{},
		genericControlPlaneResources: genericControlPlaneResources,
	}

	clusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
		DeleteFunc: func(obj interface{}) { c.deletedCluster(obj) },
	})

	apiResourceImportInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, obj interface{}) {
			c.enqueueAPIResourceImportRelatedCluster(obj)
		},
		DeleteFunc: func(obj interface{}) {
			c.enqueueAPIResourceImportRelatedCluster(obj)
		},
	})
	if err := c.apiresourceImportIndexer.AddIndexers(map[string]cache.IndexFunc{
		GVRForLocationInLogicalClusterIndexName: func(obj interface{}) ([]string, error) {
			if apiResourceImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport); ok {
				return []string{GetGVRForLocationInLogicalClusterIndexKey(apiResourceImport.Spec.Location, apiResourceImport.ClusterName, apiResourceImport.GVR())}, nil
			}
			return []string{}, nil
		},
		LocationInLogicalClusterIndexName: func(obj interface{}) ([]string, error) {
			if apiResourceImport, ok := obj.(*apiresourcev1alpha1.APIResourceImport); ok {
				return []string{GetLocationInLogicalClusterIndexKey(apiResourceImport.Spec.Location, apiResourceImport.ClusterName)}, nil
			}
			return []string{}, nil
		},
	}); err != nil {
		return nil, fmt.Errorf("Failed to add indexer for APIResourceImport: %w", err)
	}

	return c, nil
}

type Controller struct {
	queue                        workqueue.RateLimitingInterface
	apiExtensionsClient          apiextensionsclient.Interface
	kcpClient                    kcpclient.Interface
	clusterIndexer               cache.Indexer
	apiresourceImportIndexer     cache.Indexer
	syncChecks                   []cache.InformerSynced
	syncerImage                  string
	kubeconfig                   clientcmdapi.Config
	resourcesToSync              []string
	syncerMode                   SyncerMode
	syncers                      map[string]*syncer.Syncer
	apiImporters                 map[string]*APIImporter
	genericControlPlaneResources []schema.GroupVersionResource
}

func (c *Controller) enqueue(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	c.queue.Add(key)
}

func (c *Controller) enqueueAPIResourceImportRelatedCluster(obj interface{}) {
	var apiResourceImport *apiresourcev1alpha1.APIResourceImport
	switch typedObj := obj.(type) {
	case *apiresourcev1alpha1.APIResourceImport:
		apiResourceImport = typedObj
	case cache.DeletedFinalStateUnknown:
		deletedImport, ok := typedObj.Obj.(*apiresourcev1alpha1.APIResourceImport)
		if ok {
			apiResourceImport = deletedImport
		}
	}
	if apiResourceImport != nil {
		c.enqueue(&metav1.PartialObjectMetadata{
			ObjectMeta: metav1.ObjectMeta{
				Name:        apiResourceImport.Spec.Location,
				ClusterName: apiResourceImport.ClusterName,
			},
		})
	}
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.Info("Starting Cluster controller")
	defer klog.Info("Shutting down Cluster controller")

	if !cache.WaitForNamedCacheSync("cluster", ctx.Done(), c.syncChecks...) {
		klog.Warning("Failed to wait for caches to sync")
		return
	}

	for i := 0; i < numThreads; i++ {
		go wait.Until(func() { c.startWorker(ctx) }, time.Second, ctx.Done())
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
	key := k.(string)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	obj, exists, err := c.clusterIndexer.GetByKey(key)
	if err != nil {
		return err
	}

	if !exists {
		klog.Errorf("Object with key %q was deleted", key)
		return nil
	}
	current := obj.(*clusterv1alpha1.Cluster).DeepCopy()
	previous := current.DeepCopy()

	if err := c.reconcile(ctx, current); err != nil {
		return err
	}

	// If the object being reconciled changed as a result, update it.
	if !equality.Semantic.DeepEqual(previous.Status, current.Status) {
		_, uerr := c.kcpClient.ClusterV1alpha1().Clusters().UpdateStatus(ctx, current, metav1.UpdateOptions{})
		return uerr
	}

	return nil
}

func (c *Controller) deletedCluster(obj interface{}) {
	castObj, ok := obj.(*clusterv1alpha1.Cluster)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			klog.Errorf("Couldn't get object from tombstone %#v", obj)
			return
		}
		castObj, ok = tombstone.Obj.(*clusterv1alpha1.Cluster)
		if !ok {
			klog.Errorf("Tombstone contained object that is not expected %#v", obj)
			return
		}
	}
	klog.V(4).Infof("Deleting cluster %q", castObj.Name)
	ctx := context.TODO()
	c.cleanup(ctx, castObj)
}

var clusterKind = reflect.TypeOf(clusterv1alpha1.Cluster{}).Name()

func ClusterAsOwnerReference(obj *clusterv1alpha1.Cluster, controller bool) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: apiresourcev1alpha1.SchemeGroupVersion.String(),
		Kind:       clusterKind,
		Name:       obj.Name,
		UID:        obj.UID,
		Controller: &controller,
	}
}
