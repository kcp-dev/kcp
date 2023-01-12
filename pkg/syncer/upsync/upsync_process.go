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

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
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
			if err := c.upstreamClient.Resource(gvr).Cluster(clusterName.Path()).Namespace(upstreamNamespace).Delete(ctx, upstreamName, metav1.DeleteOptions{}); k8serror.IsNotFound(err) {
				return nil
			} else {
				return err
			}
		}
		if err != nil {
			return err
		}

		// Get namespaced upstream resource
		_, err = downstreamLister.ByNamespace(downstreamNamespace).Get(upstreamName)
		if k8serror.IsNotFound(err) {
			// Resource not found downstream prune it
			if err := c.upstreamClient.Resource(gvr).Cluster(clusterName.Path()).Namespace(upstreamNamespace).Delete(ctx, upstreamName, metav1.DeleteOptions{}); k8serror.IsNotFound(err) {
				return nil
			} else {
				return err
			}
		}
	} else {
		// Get cluster-wide upstream resource
		_, err = downstreamLister.Get(upstreamName)
		if k8serror.IsNotFound(err) {
			// Resource not found downstream prune it
			if err := c.upstreamClient.Resource(gvr).Cluster(clusterName.Path()).Delete(ctx, upstreamName, metav1.DeleteOptions{}); k8serror.IsNotFound(err) {
				return nil
			} else {
				return err
			}
		}
	}

	return err
}

func (c *Controller) processDownstreamResource(ctx context.Context, gvr schema.GroupVersionResource, key string, includeStatus bool) error {
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

	// retrieve downstream object
	var downstreamObject runtime.Object
	if downstreamNamespace != "" {
		downstreamObject, err = downstreamLister.ByNamespace(downstreamNamespace).Get(downstreamName)
	} else {
		downstreamObject, err = downstreamLister.Get(downstreamName)
	}
	if err != nil && !k8serror.IsNotFound(err) {
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
		// Downstream resource not present => delete resource upstream
		if err := c.upstreamClient.Resource(gvr).Cluster(upstreamLocator.ClusterName.Path()).Namespace(upstreamLocator.Namespace).Delete(ctx, downstreamName, metav1.DeleteOptions{}); k8serror.IsNotFound(err) {
			return nil
		} else {
			return err
		}
	}

	upstreamNamespace := upstreamLocator.Namespace
	upstreamWorkspace := upstreamLocator.ClusterName

	upstreamUpsyncerLister, err := c.getUpstreamUpsyncerLister(gvr)
	if err != nil {
		return err
	}

	var upstreamObject runtime.Object
	if upstreamNamespace != "" {
		upstreamObject, err = upstreamUpsyncerLister.ByCluster(upstreamWorkspace).ByNamespace(upstreamNamespace).Get(downstreamResource.GetName())
	} else {
		upstreamObject, err = upstreamUpsyncerLister.ByCluster(upstreamWorkspace).Get(downstreamResource.GetName())
	}
	if err != nil && !k8serror.IsNotFound(err) {
		return err
	}

	resourceVersionDownstream := downstreamResource.GetResourceVersion()
	if k8serror.IsNotFound(err) {
		// Resource doesn't exist upstream => let's create it
		logger.Info("Creating resource upstream", upstreamNamespace, upstreamWorkspace, downstreamResource.GetName())
		resource := prepareResourceForUpstream(ctx, gvr, upstreamNamespace, upstreamWorkspace, downstreamResource)

		if !includeStatus {
			resource.SetAnnotations(addResourceVersionAnnotation(resourceVersionDownstream, resource.GetAnnotations()))
		}

		// Create the resource
		createdResource, err := c.upstreamClient.Resource(gvr).Cluster(upstreamWorkspace.Path()).Namespace(upstreamNamespace).Create(ctx, resource, metav1.CreateOptions{})
		if err != nil {
			return err
		}

		if includeStatus {
			resource.SetResourceVersion(createdResource.GetResourceVersion())
			resource, err = c.upstreamClient.Resource(gvr).Cluster(upstreamWorkspace.Path()).Namespace(upstreamNamespace).UpdateStatus(ctx, resource, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
			resource.SetAnnotations(addResourceVersionAnnotation(resourceVersionDownstream, resource.GetAnnotations()))
			_, err = c.upstreamClient.Resource(gvr).Cluster(upstreamWorkspace.Path()).Namespace(upstreamNamespace).Update(ctx, resource, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
		}
		return nil
	}

	unstructuredUpstreamResource, ok := upstreamObject.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("upstream resource expected to be *unstructured.Unstructured got %T", upstreamObject)
	}

	resourceVersionUpstream := unstructuredUpstreamResource.GetAnnotations()[ResourceVersionAnnotation]
	if resourceVersionDownstream != resourceVersionUpstream {
		// Update Resource upstream
		logger.Info("Updating upstream resource", upstreamNamespace, upstreamWorkspace, downstreamResource)
		resource := prepareResourceForUpstream(ctx, gvr, upstreamNamespace, upstreamWorkspace, downstreamResource)
		if err != nil {
			return err
		}
		resource.SetResourceVersion(unstructuredUpstreamResource.GetResourceVersion())
		if !includeStatus {
			resource.SetAnnotations(addResourceVersionAnnotation(resourceVersionDownstream, resource.GetAnnotations()))
		} else {
			resource.SetAnnotations(addResourceVersionAnnotation(resourceVersionUpstream, resource.GetAnnotations()))
		}

		// Create the resource
		updatedResource, err := c.upstreamClient.Resource(gvr).Cluster(upstreamWorkspace.Path()).Namespace(upstreamNamespace).Update(ctx, resource, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		if includeStatus {
			resource.SetResourceVersion(updatedResource.GetResourceVersion())
			resource, err = c.upstreamClient.Resource(gvr).Cluster(upstreamWorkspace.Path()).Namespace(upstreamNamespace).UpdateStatus(ctx, resource, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
			resource.SetAnnotations(addResourceVersionAnnotation(resourceVersionDownstream, resource.GetAnnotations()))
			_, err = c.upstreamClient.Resource(gvr).Cluster(upstreamWorkspace.Path()).Namespace(upstreamNamespace).Update(ctx, resource, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
		}
		return nil
	}

	return nil
}

func (c *Controller) process(ctx context.Context, gvr schema.GroupVersionResource, key string, isUpstream, includeStatus bool) error {
	// Upstream resources are only present upstream and not downstream; so we prune them
	if isUpstream {
		return c.processUpstreamResource(ctx, gvr, key)
	} else {
		// triggers updated on upstream resource or creation of upstream resource
		return c.processDownstreamResource(ctx, gvr, key, includeStatus)
	}
}

func prepareResourceForUpstream(ctx context.Context, gvr schema.GroupVersionResource, upstreamNS string, upstreamLogicalCluster logicalcluster.Name, downstreamObj *unstructured.Unstructured) *unstructured.Unstructured {
	// Make a deepcopy
	resourceToUpsync := downstreamObj.DeepCopy()
	annotations := resourceToUpsync.GetAnnotations()
	if annotations != nil {
		delete(annotations, shared.NamespaceLocatorAnnotation)
		resourceToUpsync.SetAnnotations(annotations)
	}
	labels := resourceToUpsync.GetLabels()
	if labels != nil {
		delete(labels, workloadv1alpha1.InternalDownstreamClusterLabel)
		resourceToUpsync.SetLabels(labels)
	}
	resourceToUpsync.SetNamespace(upstreamNS)
	resourceToUpsync.SetUID("")
	resourceToUpsync.SetResourceVersion("")
	resourceToUpsync.SetManagedFields(nil)
	resourceToUpsync.SetDeletionTimestamp(nil)
	resourceToUpsync.SetDeletionGracePeriodSeconds(nil)
	resourceToUpsync.SetOwnerReferences(nil)
	resourceToUpsync.SetFinalizers(nil)

	return resourceToUpsync
}

func addResourceVersionAnnotation(resourceVersion string, annotations map[string]string) map[string]string {
	if annotations == nil {
		annotations = make(map[string]string, 1)
	}
	annotations[ResourceVersionAnnotation] = resourceVersion
	return annotations
}
