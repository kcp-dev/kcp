/*
Copyright 2023 The KCP Authors.

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

package upsync

import (
	"context"
	"fmt"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	k8serror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	. "github.com/kcp-dev/kcp/tmc/pkg/logging"
)

const (
	ResourceVersionAnnotation = "kcp.io/resource-version"
)

func (c *Controller) processUpstreamResource(ctx context.Context, gvr schema.GroupVersionResource, key string) error {
	logger := klog.FromContext(ctx)

	clusterName, upstreamNamespace, upstreamName, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "Invalid key")
		return nil
	}

	downstreamLister, err := c.getDownstreamLister(gvr)
	if err != nil {
		return err
	}

	downstreamNamespace := ""

	if upstreamNamespace != "" {
		desiredNSLocator := shared.NewNamespaceLocator(clusterName, c.syncTargetWorkspace, c.syncTargetUID, c.syncTargetName, upstreamNamespace)
		downstreamNamespace, err = shared.PhysicalClusterNamespaceName(desiredNSLocator)
		if err != nil {
			return err
		}
		// Check namespace exists
		downstreamNamespaceLister, err := c.getDownstreamLister(corev1.SchemeGroupVersion.WithResource("namespaces"))
		if err != nil {
			return err
		}
		_, err = downstreamNamespaceLister.Get(downstreamNamespace)
		if k8serror.IsNotFound(err) {
			// Downstream namespace not present; assume object is not present as well and return upstream object
			// Prune resource upstream
			return c.upstreamClient.Resource(gvr).Cluster(clusterName.Path()).Namespace(upstreamNamespace).Delete(ctx, upstreamName, metav1.DeleteOptions{})
		}
		if err != nil {
			return err
		}

		// Get namespaced upstream resource
		_, err = downstreamLister.ByNamespace(downstreamNamespace).Get(upstreamName)
		if k8serror.IsNotFound(err) {
			// Resource not found downstream prune it
			return c.upstreamClient.Resource(gvr).Cluster(clusterName.Path()).Namespace(upstreamNamespace).Delete(ctx, upstreamName, metav1.DeleteOptions{})
		}
	} else {
		// Get cluster-wide upstream resource
		_, err = downstreamLister.Get(upstreamName)
		if k8serror.IsNotFound(err) {
			// Resource not found downstream prune it
			return c.upstreamClient.Resource(gvr).Cluster(clusterName.Path()).Delete(ctx, upstreamName, metav1.DeleteOptions{})
		}
	}

	return err
}

func (c *Controller) updateResourceContent(ctx context.Context, gvr schema.GroupVersionResource, upstreamCluster logicalcluster.Name, namespace string, upstreamResource, downstreamResource *unstructured.Unstructured, field string) error {
	if field == "metadata" {
		annotations := downstreamResource.GetAnnotations()
		delete(annotations, shared.NamespaceLocatorAnnotation)
		downstreamResource.SetAnnotations(annotations)
	}
	err := unstructured.SetNestedField(upstreamResource.UnstructuredContent(), downstreamResource.UnstructuredContent()[field], field)
	if err != nil {
		return err
	}
	if field == "metadata" {
		annotations := upstreamResource.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations[ResourceVersionAnnotation] = downstreamResource.GetResourceVersion()
		upstreamResource.SetAnnotations(annotations)
	}
	_, err = c.upstreamClient.Cluster(upstreamCluster.Path()).Resource(gvr).Namespace(namespace).Update(ctx, upstreamResource, metav1.UpdateOptions{}, field)
	return err
}

func (c *Controller) processDownstreamResource(ctx context.Context, gvr schema.GroupVersionResource, key string, updateType []UpdateType) error {
	logger := klog.FromContext(ctx)
	downstreamNamespace, downstreamName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		logger.Error(err, "Invalid key")
		return nil
	}

	logger = logger.WithValues(DownstreamNamespace, downstreamNamespace, DownstreamName, downstreamName)

	downstreamLister, err := c.getDownstreamLister(gvr)
	if err != nil {
		return err
	}

	var locatorHolder metav1.Object
	if downstreamNamespace != "" {
		downstreamNamespaceLister, err := c.getDownstreamLister(corev1.SchemeGroupVersion.WithResource("namespaces"))
		if err != nil {
			return err
		}
		nsObj, err := downstreamNamespaceLister.Get(downstreamNamespace)
		if k8serror.IsNotFound(err) {
			// Since the namespace is already deleted downstream we can delete the resource upstream as well
			//
			// TODO(davidfestal): the call below won't work afaict since  the key is a downstream one, not an upstream one.
			// And in any case we don't have the locator to find out in where the upstream reosurce should be deleted.
			//
			// To manage this case we should catch downstream namespace delete events, in the resource handler,
			// and get the upstream namespace (from the locator there, to then submit the deletion of the upstream value from there)
			//
			//			return c.pruneUpstreamResource(ctx, gvr, key)
			//
			logger.Error(err, "the downstream namespace doesn't exist anymore.")
			return nil
		}
		if err != nil {
			logger.Error(err, "error getting downstream Namespace")
			return err
		}
		if nsMetav1, ok := nsObj.(metav1.Object); !ok {
			return fmt.Errorf("downstream ns expected to be metav1.Object got %T", nsObj)
		} else {
			locatorHolder = nsMetav1
		}
	}

	var downstreamObject runtime.Object
	if downstreamNamespace != "" {
		downstreamObject, err = downstreamLister.ByNamespace(downstreamNamespace).Get(downstreamName)
	} else {
		downstreamObject, err = downstreamLister.Get(downstreamName)
	}
	if err != nil && k8serror.IsNotFound(err) {
		return err
	}
	downstreamResource, ok := downstreamObject.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("Type mismatch of resource object: received %T", downstreamResource)
	}

	if downstreamNamespace == "" {
		locatorHolder = downstreamResource
	}

	var upstreamLocator *shared.NamespaceLocator
	if locatorHolder != nil {
		if locator, locatorExists, err := shared.LocatorFromAnnotations(locatorHolder.GetAnnotations()); err != nil {
			logger.Error(err, "error getting downstream locator annotation")
			return err
		} else if locatorExists && locator != nil {
			upstreamLocator = locator
		}
	}

	if upstreamLocator == nil {
		logger.Error(err, "locator not found in the downstream resource: related upstream resource cannot be updated")
		return nil
	}

	if upstreamLocator.SyncTarget.UID != c.syncTargetUID || upstreamLocator.SyncTarget.ClusterName != c.syncTargetWorkspace.String() {
		return nil
	}

	if downstreamResource == nil {
		// Downstream namespace not present; assume object is not present as well and return upstream object
		// Prune resource upstream
		return c.upstreamClient.Resource(gvr).Cluster(upstreamLocator.ClusterName.Path()).Namespace(upstreamLocator.Namespace).Delete(ctx, downstreamName, metav1.DeleteOptions{})
	}

	upstreamNamespace := upstreamLocator.Namespace
	upstreamWorkspace := upstreamLocator.ClusterName

	upstreamUpsyncerLister, err := c.getUpstreamUpsyncerLister(gvr)
	if err != nil {
		return err
	}

	upstreamResource, err := upstreamUpsyncerLister.ByCluster(upstreamWorkspace).ByNamespace(upstreamNamespace).Get(downstreamResource.GetName())
	if k8serror.IsNotFound(err) {
		// Resource doesn't exist upstream
		logger.Info("Creating resource upstream", upstreamNamespace, upstreamWorkspace, downstreamResource.GetName())
		resource, err := c.createResourceInUpstream(ctx, gvr, upstreamNamespace, upstreamWorkspace, downstreamResource)
		if err != nil {
			return err
		}
		c.updateResourceContent(ctx, gvr, upstreamWorkspace, upstreamNamespace, resource, downstreamResource, "spec")
		c.updateResourceContent(ctx, gvr, upstreamWorkspace, upstreamNamespace, resource, downstreamResource, "status")
		return nil
	}
	if err != nil {
		return err
	}

	unstructuredUpstreamResource, ok := upstreamResource.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("error: upstreal resource expected to be *unstructured.Unstructured got %T", upstreamResource)
	}

	resourceVersionUpstream := unstructuredUpstreamResource.GetAnnotations()[ResourceVersionAnnotation]
	resourceVersionDownstream := downstreamResource.GetResourceVersion()
	if resourceVersionDownstream != resourceVersionUpstream {
		// Update Resource upstream
		logger.Info("Updating upstream resource", upstreamNamespace, upstreamWorkspace, downstreamResource)
		for _, update := range updateType {
			if update == SpecUpdate {
				c.updateResourceContent(ctx, gvr, upstreamWorkspace, upstreamNamespace, unstructuredUpstreamResource, downstreamResource, "spec")
			}
			if update == StatusUpdate {
				c.updateResourceContent(ctx, gvr, upstreamWorkspace, upstreamNamespace, unstructuredUpstreamResource, downstreamResource, "status")
			}
			if update == MetadataUpdate {
				c.updateResourceContent(ctx, gvr, upstreamWorkspace, upstreamNamespace, unstructuredUpstreamResource, downstreamResource, "metadata")
			}
		}
	}

	return nil
}

func (c *Controller) process(ctx context.Context, gvr schema.GroupVersionResource, key string, isUpstream bool, updateType []UpdateType) error {
	// Upstream resources are only present upstream and not downstream; so we prune them
	if isUpstream {
		return c.processUpstreamResource(ctx, gvr, key)
	} else {
		// triggers updated on upstream resource or creation of upstream resource
		return c.processDownstreamResource(ctx, gvr, key, updateType)
	}
}

// Only add the metadata part
func (c *Controller) createResourceInUpstream(ctx context.Context, gvr schema.GroupVersionResource, upstreamNS string, upstreamLogicalCluster logicalcluster.Name, downstreamObj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	// Make a deepcopy
	downstreamObject := downstreamObj.DeepCopy()
	annotations := downstreamObject.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string, 5)
	} else {

		delete(annotations, shared.NamespaceLocatorAnnotation)
	}
	annotations[ResourceVersionAnnotation] = downstreamObj.GetResourceVersion()
	if (len(annotations)) == 0 {
		downstreamObject.SetAnnotations(nil)
	} else {
		downstreamObject.SetAnnotations(annotations)
	}
	// Create the resource
	return c.upstreamClient.Cluster(upstreamLogicalCluster.Path()).Resource(gvr).Namespace(upstreamNS).Create(ctx, downstreamObject, metav1.CreateOptions{})
}
