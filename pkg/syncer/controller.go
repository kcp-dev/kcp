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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

type mutatorGvrMap map[schema.GroupVersionResource]func(obj *unstructured.Unstructured) error
type UpsertFunc func(ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured) error
type DeleteFunc func(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error
type HandlersProvider func(c *Controller, gvr schema.GroupVersionResource) cache.ResourceEventHandlerFuncs

type Controller struct {
	name                string
	workloadClusterName string
	queue               workqueue.RateLimitingInterface

	process func(ctx context.Context, gvr schema.GroupVersionResource, key string) error

	upstreamClusterName logicalcluster.Name
}

// New returns a new syncer Controller syncing spec from "from" to "to".
func New(kcpClusterName logicalcluster.Name, pcluster string, process func(ctx context.Context, gvr schema.GroupVersionResource, key string) error, direction SyncDirection) (*Controller, error) {
	controllerName := string(direction) + "--" + kcpClusterName.String() + "--" + pcluster
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "kcp-"+controllerName)

	return &Controller{
		name:                controllerName,
		workloadClusterName: pcluster,
		queue:               queue,
		process:             process,
		upstreamClusterName: kcpClusterName,
	}, nil
}

type queueKey struct {
	gvr schema.GroupVersionResource
	key string // meta namespace key
}

func (c *Controller) AddToQueue(gvr schema.GroupVersionResource, obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	klog.Infof("Syncer %s: queueing GVR %q %s", c.name, gvr.String(), key)
	c.queue.Add(
		queueKey{
			gvr: gvr,
			key: key,
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
	qk := key.(queueKey)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, qk.gvr, qk.key); err != nil {
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
