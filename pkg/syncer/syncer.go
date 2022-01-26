/*
Copyright 2021 The KCP Authors.

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

package syncer

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

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
	"k8s.io/klog/v2"
)

const resyncPeriod = 10 * time.Hour
const SyncerNamespaceKey = "SYNCER_NAMESPACE"

type Direction string

const KcpToPcluster Direction = "kcpToPcluster"
const PclusterToKcp Direction = "pclusterToKcp"

type Syncer struct {
	specSyncer   *Controller
	statusSyncer *Controller
	Resources    sets.String
}

func (s *Syncer) Stop() {
	s.specSyncer.Stop()
	s.statusSyncer.Stop()
}

func (s *Syncer) WaitUntilDone() {
	<-s.specSyncer.Done()
	<-s.statusSyncer.Done()
}

func StartSyncer(upstream, downstream *rest.Config, resources sets.String, cluster, logicalCluster string, numSyncerThreads int) (*Syncer, error) {
	specSyncer, err := NewSpecSyncer(upstream, downstream, resources.List(), cluster, logicalCluster)
	if err != nil {
		return nil, err
	}
	statusSyncer, err := NewStatusSyncer(downstream, upstream, resources.List(), cluster, logicalCluster)
	if err != nil {
		specSyncer.Stop()
		return nil, err
	}
	specSyncer.Start(numSyncerThreads)
	statusSyncer.Start(numSyncerThreads)

	return &Syncer{
		specSyncer:   specSyncer,
		statusSyncer: statusSyncer,
		Resources:    resources,
	}, nil
}

type UpsertFunc func(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured) error
type DeleteFunc func(c *Controller, ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error
type HandlersProvider func(c *Controller, gvr schema.GroupVersionResource) cache.ResourceEventHandlerFuncs

type Controller struct {
	queue workqueue.RateLimitingInterface

	fromDSIF dynamicinformer.DynamicSharedInformerFactory

	toClient dynamic.Interface

	stopCh chan struct{}

	upsertFn  UpsertFunc
	deleteFn  DeleteFunc
	direction Direction

	namespace string
}

// New returns a new syncer Controller syncing spec from "from" to "to".
func New(fromDiscovery discovery.DiscoveryInterface, fromClient, toClient dynamic.Interface, direction Direction, syncedResourceTypes []string, clusterID string) (*Controller, error) {
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	stopCh := make(chan struct{})

	var upsertFn UpsertFunc
	var deleteFn DeleteFunc

	if direction == KcpToPcluster {
		upsertFn = upsertIntoDownstream
		deleteFn = deleteFromDownstream
	} else {
		upsertFn = updateStatusInUpstream
	}

	c := Controller{
		queue:     queue,
		toClient:  toClient,
		stopCh:    stopCh,
		direction: direction,
		namespace: os.Getenv(SyncerNamespaceKey),
		upsertFn:  upsertFn,
		deleteFn:  deleteFn,
	}

	fromDSIF := dynamicinformer.NewFilteredDynamicSharedInformerFactory(fromClient, resyncPeriod, metav1.NamespaceAll, func(o *metav1.ListOptions) {
		o.LabelSelector = fmt.Sprintf("kcp.dev/cluster=%s", clusterID)
	})

	// Get all types the upstream API server knows about.
	// TODO: watch this and learn about new types, or forget about old ones.
	gvrstrs, err := getAllGVRs(fromDiscovery, syncedResourceTypes...)
	if err != nil {
		return nil, err
	}
	for _, gvrstr := range gvrstrs {
		gvr, _ := schema.ParseResourceArg(gvrstr)

		if _, err := fromDSIF.ForResource(*gvr).Lister().List(labels.Everything()); err != nil {
			klog.Infof("Failed to list all %q: %v", gvrstr, err)
			return nil, err
		}

		fromDSIF.ForResource(*gvr).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) { c.AddToQueue(*gvr, obj) },
			UpdateFunc: func(oldObj, newObj interface{}) {
				if c.direction == KcpToPcluster {
					if !deepEqualApartFromStatus(oldObj, newObj) {
						c.AddToQueue(*gvr, newObj)
					}
				} else {
					if !deepEqualStatus(oldObj, newObj) {
						c.AddToQueue(*gvr, newObj)
					}
				}
			},
			DeleteFunc: func(obj interface{}) { c.AddToQueue(*gvr, obj) },
		})
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

func getAllGVRs(discoveryClient discovery.DiscoveryInterface, resourcesToSync ...string) ([]string, error) {
	toSyncSet := sets.NewString(resourcesToSync...)
	willBeSyncedSet := sets.NewString()
	rs, err := discoveryClient.ServerPreferredResources()
	if err != nil {
		if strings.Contains(err.Error(), "unable to retrieve the complete list of server APIs") {
			// This error may occur when some API resources added from CRDs are not completely ready.
			// We should just retry without a limit on the number of retries in such a case.
			//
			// In fact this might be related to a bug in the changes made on the feature-logical-cluster
			// Kubernetes branch to support legacy schema resources added as CRDs.
			// If this is confirmed, this test will be removed when the CRD bug is fixed.
			return nil, err
		} else {
			return nil, err
		}
	}
	gvrstrs := []string{"namespaces.v1."} // A syncer should always watch namespaces
	for _, r := range rs {
		// v1 -> v1.
		// apps/v1 -> v1.apps
		// tekton.dev/v1beta1 -> v1beta1.tekton.dev
		groupVersion, err := schema.ParseGroupVersion(r.GroupVersion)
		if err != nil {
			klog.Warningf("Unable to parse GroupVersion %s : %v", r.GroupVersion, err)
			continue
		}
		vr := groupVersion.Version + "." + groupVersion.Group
		for _, ai := range r.APIResources {
			var willBeSynced string
			groupResource := schema.GroupResource{
				Group:    groupVersion.Group,
				Resource: ai.Name,
			}

			if toSyncSet.Has(groupResource.String()) {
				willBeSynced = groupResource.String()
			} else if toSyncSet.Has(ai.Name) {
				willBeSynced = ai.Name
			} else {
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
			willBeSyncedSet.Insert(willBeSynced)
		}
	}

	notFoundResourceTypes := toSyncSet.Difference(willBeSyncedSet)
	if notFoundResourceTypes.Len() != 0 {
		// Some of the API resources expected to be there are still not published by KCP.
		// We should just retry without a limit on the number of retries in such a case,
		// until the corresponding resources are added inside KCP as CRDs and published as API resources.
		return nil, fmt.Errorf("The following resource types were requested to be synced, but were not found in the KCP logical cluster: %v", notFoundResourceTypes.List())
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

const nsDelimiter = "--"
const nsTemplate = "kcp" + nsDelimiter + "%s" + nsDelimiter + "%s"

type nameTransformer struct {
	clusterName string
	namespace   string
}

func (n *nameTransformer) toCluster() string {
	return fmt.Sprintf(nsTemplate, n.clusterName, n.namespace)
}

func toKcp(namespace string) (*nameTransformer, error) {
	segments := strings.Split(namespace, nsDelimiter)

	if len(segments) != 3 {
		return nil, fmt.Errorf("Invalid name: %s", namespace)
	}

	return &nameTransformer{
		clusterName: segments[1],
		namespace:   segments[2],
	}, nil
}

func transformForCluster(gvr schema.GroupVersionResource, meta metav1.Object) (string, string, error) {
	t := nameTransformer{
		clusterName: meta.GetClusterName(),
		namespace:   meta.GetNamespace(),
	}

	if gvr.Group == "" && gvr.Resource == "namespaces" {
		t.namespace = meta.GetName()
		return t.toCluster(), "", nil
	}

	return meta.GetName(), t.toCluster(), nil
}

func transformForKcp(gvr schema.GroupVersionResource, meta metav1.Object) (string, string, error) {
	name := meta.GetNamespace()
	if gvr.Group == "" && gvr.Resource == "namespaces" {
		name = meta.GetName()
	}

	transformer, err := toKcp(name)
	return meta.GetName(), transformer.namespace, err
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

	namespace := meta.GetNamespace()

	// This must be checked before the namespace is transformed
	if c.inSyncerNamespace(meta) {
		klog.V(2).Infof("Skipping object of type: %T : %v in syncer namespace", obj, obj)
		return nil
	}

	var targetName, targetNamespace string
	var err error
	if c.direction == KcpToPcluster {
		targetName, targetNamespace, err = transformForCluster(gvr, meta)
	} else {
		targetName, targetNamespace, err = transformForKcp(gvr, meta)
	}

	if err != nil {
		klog.Error(err)
		return err
	}

	ctx := context.TODO()

	obj, exists, err := c.fromDSIF.ForResource(gvr).Informer().GetIndexer().Get(obj)
	if err != nil {
		klog.Error(err)
		return err
	}

	if !exists && c.deleteFn != nil {
		klog.Infof("Object with gvr=%q was deleted : %s/%s", gvr, namespace, meta.GetName())
		return c.deleteFn(c, ctx, gvr, targetNamespace, targetName)
	}

	unstrob, isUnstructured := obj.(*unstructured.Unstructured)
	if !isUnstructured {
		err := fmt.Errorf("Object to synchronize is expected to be Unstructured, but is %T", obj)
		klog.Error(err)
		return err
	}

	if c.upsertFn != nil {
		unstrob = unstrob.DeepCopy()
		unstrob.SetName(targetName)
		if unstrob.GetNamespace() != "" {
			unstrob.SetNamespace(targetNamespace)
		}
		return c.upsertFn(c, ctx, gvr, targetNamespace, unstrob)
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

func (c *Controller) inSyncerNamespace(meta metav1.Object) bool {
	// If there is no value for the syncer namespace then always process the object
	// This will also handle the cluster scoped objects case for
	// objectNamespace when this is not set
	if c.namespace == "" {
		return false
	}
	var err error
	objectNamespace := meta.GetNamespace()

	if c.direction == KcpToPcluster {
		_, objectNamespace, err = transformForCluster(schema.GroupVersionResource{}, meta)
	}

	return err == nil && c.namespace == objectNamespace
}
