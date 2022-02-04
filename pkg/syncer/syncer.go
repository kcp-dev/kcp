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
	"crypto/sha256"
	"encoding/json"
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
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

const resyncPeriod = 10 * time.Hour
const SyncerNamespaceKey = "SYNCER_NAMESPACE"
const syncerApplyManager = "syncer"

// Direction indicates which direction data is flowing for this particular syncer
type Direction string

// KcpToPhysicalCluster indicates a syncer watches resources on KCP and applies the spec to the target cluster
const KcpToPhysicalCluster Direction = "kcpToPhysicalCluster"

// PhysicalClusterToKcp indicates a syncer watches resources on the target cluster and applies the status to KCP
const PhysicalClusterToKcp Direction = "physicalClusterToKcp"

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

func StartSyncer(ctx context.Context, upstream, downstream *rest.Config, resources sets.String, cluster, logicalCluster string, numSyncerThreads int) (*Syncer, error) {
	specSyncer, err := NewSpecSyncer(upstream, downstream, resources.List(), cluster, logicalCluster)
	if err != nil {
		return nil, err
	}
	statusSyncer, err := NewStatusSyncer(downstream, upstream, resources.List(), cluster, logicalCluster)
	if err != nil {
		specSyncer.Stop()
		return nil, err
	}
	specSyncer.Start(ctx, numSyncerThreads)
	statusSyncer.Start(ctx, numSyncerThreads)

	return &Syncer{
		specSyncer:   specSyncer,
		statusSyncer: statusSyncer,
		Resources:    resources,
	}, nil
}

type UpsertFunc func(ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured) error
type DeleteFunc func(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error
type HandlersProvider func(c *Controller, gvr schema.GroupVersionResource) cache.ResourceEventHandlerFuncs

type Controller struct {
	queue workqueue.RateLimitingInterface

	fromInformers dynamicinformer.DynamicSharedInformerFactory
	toClient      dynamic.Interface

	upsertFn  UpsertFunc
	deleteFn  DeleteFunc
	direction Direction

	syncerNamespace string

	// set on start
	ctx      context.Context
	cancelFn context.CancelFunc
}

// New returns a new syncer Controller syncing spec from "from" to "to".
func New(fromDiscovery discovery.DiscoveryInterface, fromClient, toClient dynamic.Interface, direction Direction, syncedResourceTypes []string, clusterID string) (*Controller, error) {
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	c := Controller{
		queue:           queue,
		toClient:        toClient,
		direction:       direction,
		syncerNamespace: os.Getenv(SyncerNamespaceKey),
	}

	if direction == KcpToPhysicalCluster {
		c.upsertFn = c.applyToDownstream
		c.deleteFn = c.deleteFromDownstream
	} else {
		c.upsertFn = c.updateStatusInUpstream
	}

	fromInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(fromClient, resyncPeriod, metav1.NamespaceAll, func(o *metav1.ListOptions) {
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

		if _, err := fromInformers.ForResource(*gvr).Lister().List(labels.Everything()); err != nil {
			klog.Infof("Failed to list all %q: %v", gvrstr, err)
			return nil, err
		}

		fromInformers.ForResource(*gvr).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) { c.AddToQueue(*gvr, obj) },
			UpdateFunc: func(oldObj, newObj interface{}) {
				if c.direction == KcpToPhysicalCluster {
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

	c.fromInformers = fromInformers

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
func (c *Controller) Start(ctx context.Context, numThreads int) {
	c.ctx, c.cancelFn = context.WithCancel(ctx)

	c.fromInformers.Start(c.ctx.Done())
	c.fromInformers.WaitForCacheSync(c.ctx.Done())

	for i := 0; i < numThreads; i++ {
		go c.startWorker(c.ctx)
	}
}

// startWorker processes work items until stopCh is closed.
func (c *Controller) startWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			klog.Info("stopping syncer worker")
			return
		default:
			c.processNextWorkItem(ctx)
		}
	}
}

// Stop stops the syncer.
func (c *Controller) Stop() {
	c.queue.ShutDown()
	c.cancelFn()
}

// Done returns a channel that's closed when the syncer is stopped.
func (c *Controller) Done() <-chan struct{} { return c.ctx.Done() }

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	i, quit := c.queue.Get()
	if quit {
		return false
	}
	h := i.(holder)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(i)

	err := c.process(ctx, h.gvr, h.obj)
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

// NamespaceLocator stores a logical cluster and namespace and is used
// as the source for the mapped namespace name in a physical cluster.
type NamespaceLocator struct {
	LogicalCluster string `json:"logical-cluster"`
	Namespace      string `json:"namespace"`
}

// PhysicalClusterNamespaceName encodes the NamespaceLocator to a new
// namespace name for use on a physical cluster. The encoding is repeatable.
func PhysicalClusterNamespaceName(l NamespaceLocator) (string, error) {
	b, err := json.Marshal(l)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum224(b)
	return fmt.Sprintf("kcp%x", hash), nil
}

func (c *Controller) process(ctx context.Context, gvr schema.GroupVersionResource, obj interface{}) error {
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

	if namespace == "" || namespace == c.syncerNamespace {
		// skipping cluster-level objects and those in syncer namespace
		// TODO: why do we watch cluster level objects?!
		return nil
	}

	var (
		targetName      = meta.GetName()
		targetNamespace string
		err             error
	)
	if c.direction == KcpToPhysicalCluster {
		l := NamespaceLocator{
			LogicalCluster: meta.GetClusterName(),
			Namespace:      namespace,
		}
		targetNamespace, err = PhysicalClusterNamespaceName(l)
		if err != nil {
			klog.Errorf("error hashing namespace: %v", err)
			return nil
		}
	} else {
		nsInformer := c.fromInformers.ForResource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
		nsObj, err := nsInformer.Lister().Get(clusters.ToClusterAwareKey(meta.GetClusterName(), namespace))
		if err != nil {
			klog.Errorf("error listing namespace %q from the physical cluster: %v", namespace, err)
			return nil
		}
		nsMeta, ok := nsObj.(metav1.Object)
		if !ok {
			klog.Errorf("namespace %q: expected metav1.Object, got %T", namespace, nsObj)
			return nil
		}
		if namespaceLocationAnnotation := nsMeta.GetAnnotations()[namespaceLocatorAnnotation]; namespaceLocationAnnotation != "" {
			var l NamespaceLocator
			if err := json.Unmarshal([]byte(namespaceLocationAnnotation), &l); err != nil {
				klog.Errorf("namespace %q: error decoding annotation: %v", namespace, err)
				return nil
			}
			targetNamespace = l.Namespace
		} else {
			// this is not our namespace, silently skipping
			return nil
		}
	}

	obj, exists, err := c.fromInformers.ForResource(gvr).Informer().GetIndexer().Get(obj)
	if err != nil {
		klog.Error(err)
		return err
	}

	if !exists {
		klog.Infof("Object with gvr=%q was deleted : %s/%s", gvr, namespace, meta.GetName())
		if c.deleteFn != nil {
			return c.deleteFn(ctx, gvr, targetNamespace, targetName)
		}
		return nil
	}

	unstrob, isUnstructured := obj.(*unstructured.Unstructured)
	if !isUnstructured {
		err := fmt.Errorf("object to synchronize is expected to be Unstructured, but is %T", obj)
		klog.Error(err)
		return err
	}

	if c.upsertFn != nil {
		return c.upsertFn(ctx, gvr, targetNamespace, unstrob)
	}

	return err
}
