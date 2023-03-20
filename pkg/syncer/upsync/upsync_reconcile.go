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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"

	k8serror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
	. "github.com/kcp-dev/kcp/tmc/pkg/logging"
)

const (
	// ResourceVersionAnnotation is an annotation set on a resource upsynced upstream
	// that contains the resourceVersion of the corresponding downstream resource
	// when it was last upsynced.
	// It is used to check easily, without having to compare the resource contents,
	// whether an upsynced upstream resource is up-to-date with the downstream resource.
	ResourceVersionAnnotation = "workload.kcp.io/rv"
)

func (c *controller) reconcile(ctx context.Context, upstreamObject *unstructured.Unstructured, gvr schema.GroupVersionResource, upstreamClusterName logicalcluster.Name, upstreamNamespace, upstreamName string, dirtyStatus bool) (bool, error) {
	logger := klog.FromContext(ctx)

	upstreamClient, err := c.getUpstreamClient(upstreamClusterName)
	if err != nil {
		return false, err
	}

	downstreamNamespace := ""
	if upstreamNamespace != "" {
		// find downstream namespace through locator index
		locator := shared.NewNamespaceLocator(upstreamClusterName, c.syncTargetClusterName, c.syncTargetUID, c.syncTargetName, upstreamNamespace)
		locatorValue, err := json.Marshal(locator)
		if err != nil {
			return false, err
		}
		downstreamNamespaces, err := c.listDownstreamNamespacesByLocator(string(locatorValue))
		if err != nil {
			return false, err
		}
		if len(downstreamNamespaces) == 1 {
			namespace := downstreamNamespaces[0]
			logger.WithValues(DownstreamName, namespace.GetName()).V(4).Info("Found downstream namespace for upstream namespace")
			downstreamNamespace = namespace.GetName()
		} else if len(downstreamNamespaces) > 1 {
			// This should never happen unless there's some namespace collision.
			var namespacesCollisions []string
			for _, namespace := range downstreamNamespaces {
				namespacesCollisions = append(namespacesCollisions, namespace.GetName())
			}
			return false, fmt.Errorf("(namespace collision) found multiple downstream namespaces: %s for upstream namespace %s|%s", strings.Join(namespacesCollisions, ","), upstreamClusterName, upstreamNamespace)
		} else {
			logger.V(4).Info("No downstream namespaces found")
			// Downstream resource namespace not present => delete resource upstream
			if err := c.deleteUpstreamResource(ctx, gvr, upstreamClusterName, upstreamNamespace, upstreamName, true); err != nil && !k8serror.IsNotFound(err) {
				return false, err
			}
			return false, nil
		}
	}

	logger = logger.WithValues(DownstreamNamespace, downstreamNamespace)
	ctx = klog.NewContext(ctx, logger)

	// retrieve downstream object

	downstreamLister, err := c.getDownstreamLister(gvr)
	if err != nil {
		return false, err
	}

	var downstreamObject runtime.Object
	if downstreamNamespace != "" {
		downstreamObject, err = downstreamLister.ByNamespace(downstreamNamespace).Get(upstreamName)
	} else {
		downstreamObject, err = downstreamLister.Get(upstreamName)
	}
	if k8serror.IsNotFound(err) {
		// Downstream resource not present => delete resource upstream
		if err := c.deleteUpstreamResource(ctx, gvr, upstreamClusterName, upstreamNamespace, upstreamName, true); err != nil && !k8serror.IsNotFound(err) {
			return false, err
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}

	downstreamResource, ok := downstreamObject.(*unstructured.Unstructured)
	if !ok {
		return false, fmt.Errorf("type mismatch of resource object: received %T", downstreamResource)
	}
	downstreamRV := downstreamResource.GetResourceVersion()
	markedForDeletionDownstream := downstreamResource.GetDeletionTimestamp() != nil

	// (potentially) create object upstream
	if upstreamObject == nil {
		if markedForDeletionDownstream {
			return false, nil
		}
		logger.V(1).Info("Creating resource upstream")
		preparedResource := c.prepareResourceForUpstream(ctx, gvr, upstreamNamespace, upstreamClusterName, downstreamResource)

		if !dirtyStatus {
			// if no status needs to be upsynced upstream, then we can set the resource version annotation at the same time as we create the
			// resource
			preparedResource.SetAnnotations(addResourceVersionAnnotation(downstreamRV, preparedResource.GetAnnotations()))
			// Create the resource
			_, err := upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Create(ctx, preparedResource, metav1.CreateOptions{})
			return false, err
		}

		// Status also needs to be upsynced so let's do it in 3 steps:
		// - create the resource
		createdResource, err := upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Create(ctx, preparedResource, metav1.CreateOptions{})
		if err != nil {
			return false, err
		}
		// - update the status as a distinct action,
		preparedResource.SetResourceVersion(createdResource.GetResourceVersion())
		updatedResource, err := upstreamClient.Resource(gvr).Namespace(upstreamNamespace).UpdateStatus(ctx, preparedResource, metav1.UpdateOptions{})
		if err != nil {
			return false, err
		}
		// - finally update the main content again to set the resource version annotation to the value of the downstream resource version
		preparedResource.SetAnnotations(addResourceVersionAnnotation(downstreamRV, preparedResource.GetAnnotations()))
		preparedResource.SetResourceVersion(updatedResource.GetResourceVersion())
		_, err = upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Update(ctx, preparedResource, metav1.UpdateOptions{})
		return false, err
	}

	// update upstream when annotation RV differs
	resourceVersionUpstream := upstreamObject.GetAnnotations()[ResourceVersionAnnotation]
	if downstreamRV != resourceVersionUpstream {
		logger.V(1).Info("Updating upstream resource")
		preparedResource := c.prepareResourceForUpstream(ctx, gvr, upstreamNamespace, upstreamClusterName, downstreamResource)
		if err != nil {
			return false, err
		}

		// quick path: status unchanged, only update main resource
		if !dirtyStatus {
			preparedResource.SetAnnotations(addResourceVersionAnnotation(downstreamRV, preparedResource.GetAnnotations()))
			existingResource, err := upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Get(ctx, preparedResource.GetName(), metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			preparedResource.SetResourceVersion(existingResource.GetResourceVersion())
			_, err = upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Update(ctx, preparedResource, metav1.UpdateOptions{})
			// If the downstream resource is marked for deletion, let's requeue it to manage the deletion timestamp
			return markedForDeletionDownstream, err
		}

		// slow path: status changed => we need 3 steps
		// 1. update main resource

		preparedResource.SetAnnotations(addResourceVersionAnnotation(resourceVersionUpstream, preparedResource.GetAnnotations()))
		existingResource, err := upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Get(ctx, preparedResource.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		preparedResource.SetResourceVersion(existingResource.GetResourceVersion())
		updatedResource, err := upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Update(ctx, preparedResource, metav1.UpdateOptions{})
		if err != nil {
			return false, err
		}

		// 2. update the status as a distinct action,
		preparedResource.SetResourceVersion(updatedResource.GetResourceVersion())
		updatedResource, err = upstreamClient.Resource(gvr).Namespace(upstreamNamespace).UpdateStatus(ctx, preparedResource, metav1.UpdateOptions{})
		if err != nil {
			return false, err
		}

		// 3. finally update the main resource again to set the resource version annotation to the value of the downstream resource version.
		preparedResource.SetAnnotations(addResourceVersionAnnotation(downstreamRV, preparedResource.GetAnnotations()))
		preparedResource.SetResourceVersion(updatedResource.GetResourceVersion())
		_, err = upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Update(ctx, preparedResource, metav1.UpdateOptions{})
		// If the downstream resource is marked for deletion, let's requeue it to manage the deletion timestamp
		return markedForDeletionDownstream, err
	}

	if downstreamResource.GetDeletionTimestamp() != nil {
		if err := c.deleteUpstreamResource(ctx, gvr, upstreamClusterName, upstreamNamespace, upstreamName, false); err != nil && !k8serror.IsNotFound(err) {
			return false, err
		}
	}

	return false, nil
}

func (c *controller) prepareResourceForUpstream(ctx context.Context, gvr schema.GroupVersionResource, upstreamNS string, upstreamLogicalCluster logicalcluster.Name, downstreamObj *unstructured.Unstructured) *unstructured.Unstructured {
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
	resourceToUpsync.SetFinalizers([]string{shared.SyncerFinalizerNamePrefix + c.syncTargetKey})

	return resourceToUpsync
}

func (c *controller) deleteUpstreamResource(ctx context.Context, gvr schema.GroupVersionResource, clusterName logicalcluster.Name, namespace, name string, force bool) error {
	upstreamClient, err := c.getUpstreamClient(clusterName)
	if err != nil {
		return err
	}

	if force {
		existingResource, err := upstreamClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if k8serror.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(existingResource.GetFinalizers()) > 0 {
			existingResource.SetFinalizers(nil)
			if _, err := upstreamClient.Resource(gvr).Namespace(namespace).Update(ctx, existingResource, metav1.UpdateOptions{}); err != nil {
				return err
			}
		}
	}
	return upstreamClient.Resource(gvr).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
}

func addResourceVersionAnnotation(resourceVersion string, annotations map[string]string) map[string]string {
	if annotations == nil {
		annotations = make(map[string]string, 1)
	}
	annotations[ResourceVersionAnnotation] = resourceVersion
	return annotations
}
