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

package upsync

import (
	"context"
	"errors"
	"fmt"

	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	. "github.com/kcp-dev/kcp/tmc/pkg/logging"
	"github.com/kcp-dev/logicalcluster/v3"
	k8serror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

const (
	ResourceVersionAnnotation = "kcp.dev/resource-version"
)

func (c *Controller) pruneUpstreamResource(ctx context.Context, gvr schema.GroupVersionResource, key string) error {
	clusterNameString, upstreamNamespace, upstreamName, err := ParseUpstreamKey(key)
	if err != nil {
		return err
	}
	clusterName := logicalcluster.New(clusterNameString)
	err = c.upstreamClient.Cluster(clusterName).Resource(gvr).Namespace(upstreamNamespace).Delete(ctx, upstreamName, metav1.DeleteOptions{})
	if err != nil {
		return err
	}
	return nil
}

func (c *Controller) processUpstreamResource(ctx context.Context, gvr schema.GroupVersionResource, key string) error {
	logger := klog.FromContext(ctx)
	clusterNameString, upstreamNamespace, upstreamName, err := ParseUpstreamKey(key)
	// ClusterFromContext can be used here???
	clusterName := logicalcluster.New(clusterNameString)
	if err != nil {
		logger.Error(err, "Invalid key")
		return nil
	}
	// Get upstream resource
	desiredNSLocator := shared.NewNamespaceLocator(clusterName, c.syncTargetWorkspace, c.syncTargetUID, c.syncTargetName, upstreamNamespace)
	if upstreamNamespace != "" {
		downstreamNamespace, err := shared.PhysicalClusterNamespaceName(desiredNSLocator)
		if err != nil {
			return err
		}
		// Check namespace exists
		_, err = c.downstreamNamespaceLister.Get(downstreamNamespace)
		if err != nil && k8serror.IsNotFound(err) {
			// Downstream namespace not present; assume object is not present as well and return upstream object
			// Prune resource upstream
			return c.pruneUpstreamResource(ctx, gvr, key)
		}
		if err != nil && !k8serror.IsNotFound(err) {
			return err
		}
		_, err = c.downstreamNamespaceLister.ByNamespace(downstreamNamespace).Get(upstreamName)
		if err != nil && k8serror.IsNotFound(err) {
			// Resource not found downstream prune it
			return c.pruneUpstreamResource(ctx, gvr, key)
		}
		if err != nil && !k8serror.IsNotFound(err) {
			return err
		}
	} else {
		_, err := c.downstreamInformer.ForResource(gvr).Lister().Get(upstreamName)
		if err != nil && k8serror.IsNotFound(err) {
			// Downstream namespace not present; assume object is not present as well and return upstream object
			// Prune resource upstream
			return c.pruneUpstreamResource(ctx, gvr, key)
		}
		if err != nil && !k8serror.IsNotFound(err) {
			return err
		}
	}
	return nil
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
	_, err = c.upstreamClient.Cluster(upstreamCluster).Resource(gvr).Namespace(namespace).Update(ctx, upstreamResource, metav1.UpdateOptions{}, field)
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

	var upstreamLocator *shared.NamespaceLocator
	var locatorExists bool
	var downstreamObject interface{}
	// Namespace scoped resource
	if downstreamNamespace != "" {
		var nsObj runtime.Object
		nsObj, err = c.downstreamNamespaceLister.Get(downstreamNamespace)
		if err != nil && k8serror.IsNotFound(err) {
			// Since the namespace is already deleted downstream we can delete the resource upstream as well
			c.pruneUpstreamResource(ctx, gvr, key)
		}
		if err != nil && !k8serror.IsNotFound(err) {
			logger.Error(err, "Error getting downstream Namespace from downstream namespace lister", "ns", downstreamNamespace)
			return err
		}
		nsMeta, ok := nsObj.(metav1.Object)
		if !ok {
			logger.Info(fmt.Sprintf("Error: downstream ns expected to be metav1.Object got %T", nsObj))
		}

		upstreamLocator, locatorExists, err = shared.LocatorFromAnnotations(nsMeta.GetAnnotations())
		if err != nil {
			logger.Error(err, "Error ")
		}

		if !locatorExists || upstreamLocator == nil {
			return nil
		}
		downstreamObject, err = c.downstreamInformer.ForResource(gvr).Lister().ByNamespace(downstreamNamespace).Get(downstreamName)
		if err != nil {
			logger.Error(err, "Could not find the resource downstream")
		}
	} else {
		downstreamObject, err = c.downstreamInformer.ForResource(gvr).Lister().Get(downstreamName)
		if err != nil && k8serror.IsNotFound(err) {
			logger.Error(err, "Could not find the resource downstream")
			// Todo: Perform cleanup on the upstream resource
			return nil
		}
		objMeta, ok := downstreamObject.(metav1.Object)
		if !ok {
			logger.Info("Error: downstream cluster-wide res not of metav1 type")
		}
		if upstreamLocator, locatorExists, err = shared.LocatorFromAnnotations(objMeta.GetAnnotations()); err != nil {
			logger.Error(err, "Error decoding annotations on DS cluster-wide res")
		}

		if !locatorExists || upstreamLocator == nil {
			logger.Error(err, "locator not found in the resource")
			return errors.New("locator not found in the downstream resource")
		}
	}

	downstreamResource, ok := downstreamObject.(*unstructured.Unstructured)
	if !ok {
		logger.Info(fmt.Sprintf("Type mismatch of resource object. Recieved %T", downstreamResource))
		return nil
	}

	if upstreamLocator.SyncTarget.UID != c.syncTargetUID || upstreamLocator.SyncTarget.Workspace != c.syncTargetWorkspace.String() {
		return nil
	}

	upstreamNamespace := upstreamLocator.Namespace
	upstreamWorkspace := upstreamLocator.Workspace

	upstreamResource, err := c.upstreamInformer.ForResource(gvr).Lister().ByCluster(upstreamWorkspace).ByNamespace(upstreamNamespace).Get(downstreamResource.GetName())
	unstructuredUpstreamResource := upstreamResource.(*unstructured.Unstructured)
	resourceVersionUpstream := ""
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
	} else {
		resourceVersionUpstream = unstructuredUpstreamResource.GetAnnotations()[ResourceVersionAnnotation]
	}
	if !k8serror.IsNotFound(err) && err != nil {
		return err
	}

	resourceVersionDownstream := downstreamResource.GetResourceVersion()
	if unstructuredUpstreamResource != nil && resourceVersionDownstream != resourceVersionUpstream {
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
	return c.upstreamClient.Cluster(upstreamLogicalCluster).Resource(gvr).Namespace(upstreamNS).Create(ctx, downstreamObject, metav1.CreateOptions{})
}
