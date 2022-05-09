/*
Copyright 2022 The KCP Authors.

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
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	workloadcliplugin "github.com/kcp-dev/kcp/pkg/cliplugins/workload/plugin"
)

type mutatorGvrMap map[schema.GroupVersionResource]func(obj *unstructured.Unstructured) error
type UpsertFunc func(ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured) error
type DeleteFunc func(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error
type HandlersProvider func(c *Controller, gvr schema.GroupVersionResource) cache.ResourceEventHandlerFuncs

type Controller struct {
	name                string
	workloadClusterName string
	queue               workqueue.RateLimitingInterface

	fromInformers dynamicinformer.DynamicSharedInformerFactory
	fromClient    dynamic.Interface
	toClient      dynamic.Interface

	upsertFn  UpsertFunc
	deleteFn  DeleteFunc
	direction SyncDirection

	upstreamClusterName logicalcluster.Name
	mutators            mutatorGvrMap

	advancedSchedulingEnabled bool
}

// New returns a new syncer Controller syncing spec from "from" to "to".
func New(kcpClusterName logicalcluster.Name, pcluster string, fromClient, toClient dynamic.Interface, fromInformers dynamicinformer.DynamicSharedInformerFactory,
	direction SyncDirection, gvrs []string, pclusterID string, mutators mutatorGvrMap, advancedSchedulingEnabled bool) (*Controller, error) {
	controllerName := string(direction) + "--" + kcpClusterName.String() + "--" + pcluster
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "kcp-"+controllerName)

	c := Controller{
		name:                      controllerName,
		workloadClusterName:       pcluster,
		queue:                     queue,
		toClient:                  toClient,
		fromClient:                fromClient,
		direction:                 direction,
		upstreamClusterName:       kcpClusterName,
		mutators:                  make(mutatorGvrMap),
		advancedSchedulingEnabled: advancedSchedulingEnabled,
	}

	if len(mutators) > 0 {
		c.mutators = mutators
	}

	if direction == SyncDown {
		c.upsertFn = c.applyToDownstream
		c.deleteFn = c.deleteFromDownstream
	} else {
		c.upsertFn = c.updateStatusInUpstream
		c.deleteFn = func(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error {
			if c.advancedSchedulingEnabled {
				return ensureUpstreamFinalizerRemoved(ctx, gvr, c.toClient, namespace, c.workloadClusterName, c.upstreamClusterName, name)
			}
			return nil
		}
	}

	for _, gvrstr := range gvrs {
		gvr, _ := schema.ParseResourceArg(gvrstr)

		fromInformers.ForResource(*gvr).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				c.AddToQueue(*gvr, obj)
			},
			UpdateFunc: func(oldObj, newObj interface{}) {
				oldUnstrob := oldObj.(*unstructured.Unstructured)
				newUnstrob := newObj.(*unstructured.Unstructured)

				if c.direction == SyncDown {
					if !deepEqualApartFromStatus(oldUnstrob, newUnstrob) {
						c.AddToQueue(*gvr, newUnstrob)
					}
				} else {
					if !deepEqualFinalizersAndStatus(oldUnstrob, newUnstrob) {
						c.AddToQueue(*gvr, newUnstrob)
					}
				}
			},
			DeleteFunc: func(obj interface{}) {
				c.AddToQueue(*gvr, obj)
			},
		})
		klog.InfoS("Set up informer", "direction", c.direction, "clusterName", kcpClusterName, "pcluster", pcluster, "gvr", gvr)
	}

	c.fromInformers = fromInformers

	return &c, nil
}

type holder struct {
	gvr         schema.GroupVersionResource
	clusterName logicalcluster.Name
	namespace   string
	name        string
}

func (c *Controller) AddToQueue(gvr schema.GroupVersionResource, obj interface{}) {
	objToCheck := obj

	tombstone, ok := objToCheck.(cache.DeletedFinalStateUnknown)
	if ok {
		objToCheck = tombstone.Obj
	}

	metaObj, err := meta.Accessor(objToCheck)
	if err != nil {
		klog.Errorf("%s: error getting meta for %T", c.name, obj)
		return
	}

	qualifiedName := metaObj.GetName()
	if len(metaObj.GetNamespace()) > 0 {
		qualifiedName = metaObj.GetNamespace() + "/" + qualifiedName
	}
	if c.direction == SyncDown {
		qualifiedName = metaObj.GetClusterName() + "|" + qualifiedName
	}
	klog.Infof("Syncer %s: adding %s %s to queue", c.name, gvr, qualifiedName)

	c.queue.Add(
		holder{
			gvr:         gvr,
			clusterName: logicalcluster.From(metaObj),
			namespace:   metaObj.GetNamespace(),
			name:        metaObj.GetName(),
		},
	)
}

// Start starts N worker processes processing work items.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	klog.InfoS("Starting syncer workers", "controller", c.name)
	defer klog.InfoS("Stopping syncer workers", "controller", c.name)
	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

// startWorker processes work items until stopCh is closed.
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
	h := key.(holder)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, h); err != nil {
		runtime.HandleError(fmt.Errorf("syncer %q failed to sync %q, err: %w", c.name, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}

// NamespaceLocator stores a logical cluster and namespace and is used
// as the source for the mapped namespace name in a physical cluster.
type NamespaceLocator struct {
	LogicalCluster logicalcluster.Name `json:"logical-cluster"`
	Namespace      string              `json:"namespace"`
}

func LocatorFromAnnotations(annotations map[string]string) (*NamespaceLocator, error) {
	annotation := annotations[namespaceLocatorAnnotation]
	if len(annotation) == 0 {
		return nil, nil
	}
	var locator NamespaceLocator
	if err := json.Unmarshal([]byte(annotation), &locator); err != nil {
		return nil, err
	}
	return &locator, nil
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

func (c *Controller) process(ctx context.Context, h holder) error {
	klog.V(2).InfoS("Processing", "gvr", h.gvr, "clusterName", h.clusterName, "namespace", h.namespace, "name", h.name)

	if h.namespace == "" {
		// skipping cluster-level objects and those in syncer namespace
		// TODO: why do we watch cluster level objects?!
		return nil
	}

	var (
		// No matter which direction we're going, when we're trying to retrieve a (potentially existing) object,
		// always use its namespace, without any mapping. This is the namespace that came from the event from
		// the shared informer.
		fromNamespace = h.namespace

		// We do need to map the namespace in which we are creating or updating an object
		toNamespace string
	)

	// Determine toNamespace
	if c.direction == SyncDown {
		// Convert the clusterName and namespace to a single string for toNamespace
		l := NamespaceLocator{
			LogicalCluster: h.clusterName,
			Namespace:      h.namespace,
		}

		var err error
		toNamespace, err = PhysicalClusterNamespaceName(l)
		if err != nil {
			klog.Errorf("%s: error hashing namespace: %v", c.name, err)
			return nil
		}
	} else {
		if strings.HasPrefix(workloadcliplugin.SyncerIDPrefix, h.namespace) {
			// skip syncer namespace
			return nil
		}

		nsInformer := c.fromInformers.ForResource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})

		nsKey := fromNamespace
		if !h.clusterName.Empty() {
			// If our "physical" cluster is a kcp instance (e.g. for testing purposes), it will return resources
			// with metadata.clusterName set, which means their keys are cluster-aware, so we need to do the same here.
			nsKey = clusters.ToClusterAwareKey(h.clusterName, nsKey)
		}

		nsObj, err := nsInformer.Lister().Get(nsKey)
		if err != nil {
			klog.Errorf("%s: error retrieving namespace %q from physical cluster lister: %v", c.name, nsKey, err)
			return nil
		}

		nsMeta, ok := nsObj.(metav1.Object)
		if !ok {
			klog.Errorf("%s: namespace %q: expected metav1.Object, got %T", c.name, nsKey, nsObj)
			return nil
		}

		namespaceLocator, err := LocatorFromAnnotations(nsMeta.GetAnnotations())
		if err != nil {
			klog.Errorf("%s: namespace %q: error decoding annotation: %v", c.name, nsKey, err)
			return nil
		}

		if namespaceLocator != nil && namespaceLocator.LogicalCluster == c.upstreamClusterName {
			// Only sync resources for the configured logical cluster to ensure
			// that syncers for multiple logical clusters can coexist.
			toNamespace = namespaceLocator.Namespace
		} else {
			// this is not our namespace, silently skipping
			return nil
		}
	}

	key := h.name

	if !h.clusterName.Empty() {
		key = clusters.ToClusterAwareKey(h.clusterName, key)
	}

	key = fromNamespace + "/" + key

	obj, exists, err := c.fromInformers.ForResource(h.gvr).Informer().GetIndexer().GetByKey(key)
	if err != nil {
		return err
	}

	if !exists {
		klog.InfoS("Object doesn't exist:", "direction", c.direction, "clusterName", h.clusterName, "namespace", fromNamespace, "name", h.name)
		if c.deleteFn != nil {
			return c.deleteFn(ctx, h.gvr, toNamespace, h.name)
		}
		return nil
	}

	unstrob, isUnstructured := obj.(*unstructured.Unstructured)
	if !isUnstructured {
		return fmt.Errorf("%s: object to synchronize is expected to be Unstructured, but is %T", c.name, obj)
	}

	if c.upsertFn != nil {
		return c.upsertFn(ctx, h.gvr, toNamespace, unstrob)
	}

	return err
}

// transformName changes the object name into the desired one based on the Direction:
// - if the object is a configmap it handles the "kube-root-ca.crt" name mapping
// - if the object is a serviceaccount it handles the "default" name mapping
func transformName(syncedObject *unstructured.Unstructured, direction SyncDirection) {
	configMapGVR := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}
	serviceAccountGVR := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ServiceAccount"}
	secretGVR := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"}

	switch direction {
	case SyncDown:
		if syncedObject.GroupVersionKind() == configMapGVR && syncedObject.GetName() == "kube-root-ca.crt" {
			syncedObject.SetName("kcp-root-ca.crt")
		}
		if syncedObject.GroupVersionKind() == serviceAccountGVR && syncedObject.GetName() == "default" {
			syncedObject.SetName("kcp-default")
		}
		// TODO(jmprusi): We are rewriting the name of the object into a non random one so we can reference it from the deployment transformer
		//                but this means that means than more than one default-token-XXXX object will overwrite the same "kcp-default-token"
		//				  object. This must be fixed.
		if syncedObject.GroupVersionKind() == secretGVR && strings.Contains(syncedObject.GetName(), "default-token-") {
			syncedObject.SetName("kcp-default-token")
		}
	case SyncUp:
		if syncedObject.GroupVersionKind() == configMapGVR && syncedObject.GetName() == "kcp-root-ca.crt" {
			syncedObject.SetName("kube-root-ca.crt")
		}
		if syncedObject.GroupVersionKind() == serviceAccountGVR && syncedObject.GetName() == "kcp-default" {
			syncedObject.SetName("default")
		}
	}
}

func ensureUpstreamFinalizerRemoved(ctx context.Context, gvr schema.GroupVersionResource, upstreamClient dynamic.Interface, upstreamNamespace, workloadClusterName string, logicalClusterName logicalcluster.Name, resourceName string) error {
	upstreamObj, err := upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Get(ctx, resourceName, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if errors.IsNotFound(err) {
		return nil
	}

	// TODO(jmprusi): This check will need to be against "GetDeletionTimestamp()" when using the syncer virtual  workspace.
	if upstreamObj.GetAnnotations()[workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+workloadClusterName] == "" {
		// Do nothing: the object should not be deleted anymore for this location on the KCP side
		return nil
	}

	// Remove the syncer finalizer.
	currentFinalizers := upstreamObj.GetFinalizers()
	desiredFinalizers := []string{}
	for _, finalizer := range currentFinalizers {
		if finalizer != syncerFinalizerNamePrefix+workloadClusterName {
			desiredFinalizers = append(desiredFinalizers, finalizer)
		}
	}
	upstreamObj.SetFinalizers(desiredFinalizers)

	//  TODO(jmprusi): This code block will be handled by the syncer virtual workspace, so we can remove it once
	//                 the virtual workspace syncer is integrated
	//  - Begin -
	// Clean up the status annotation and the locationDeletionAnnotation.
	annotations := upstreamObj.GetAnnotations()
	delete(annotations, workloadv1alpha1.InternalClusterStatusAnnotationPrefix+workloadClusterName)
	delete(annotations, workloadv1alpha1.InternalClusterDeletionTimestampAnnotationPrefix+workloadClusterName)
	delete(annotations, workloadv1alpha1.InternalClusterStatusAnnotationPrefix+workloadClusterName)
	upstreamObj.SetAnnotations(annotations)

	// remove the cluster label.
	upstreamLabels := upstreamObj.GetLabels()
	delete(upstreamLabels, workloadv1alpha1.InternalClusterResourceStateLabelPrefix+workloadClusterName)
	upstreamObj.SetLabels(upstreamLabels)
	// - End of block to be removed once the virtual workspace syncer is integrated -

	if _, err := upstreamClient.Resource(gvr).Namespace(upstreamObj.GetNamespace()).Update(ctx, upstreamObj, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("Failed updating after removing the finalizers of resource %s|%s/%s: %v", logicalClusterName, upstreamNamespace, upstreamObj.GetName(), err)
		return err
	}
	klog.V(2).Infof("Updated resource %s|%s/%s after removing the finalizers", logicalClusterName, upstreamNamespace, upstreamObj.GetName())
	return nil
}
