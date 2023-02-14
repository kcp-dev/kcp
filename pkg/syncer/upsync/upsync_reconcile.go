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

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
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

	downstreamNamespace := ""
	if upstreamNamespace != "" {
		// retrieve the downstream namespace from the upstream namespace through the namespace locator index
		namespaceLocator := shared.NewNamespaceLocator(upstreamClusterName, c.syncTargetClusterName, c.syncTargetUID, c.syncTargetName, upstreamNamespace)
		locatorAnnotationValue, err := json.Marshal(namespaceLocator)
		if err != nil {
			return false, err
		}
		downstreamNamespaces, err := c.listDownstreamNamespacesByLocator(string(locatorAnnotationValue))
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
			return false, fmt.Errorf("(namespace collision) found multiple downstream namespaces: %s for upstream namespace %s|%s", strings.Join(namespacesCollisions, ","), upstreamClusterName, upstreamObject.GetNamespace())
		} else {
			logger.V(4).Info("No downstream namespaces found")
			// Downstream resource namespace not present => delete resource upstream
			return false, c.removeUpstreamResource(ctx, gvr, upstreamClusterName, upstreamNamespace, upstreamName, true)
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
		return false, c.removeUpstreamResource(ctx, gvr, upstreamClusterName, upstreamObject.GetNamespace(), upstreamObject.GetName(), true)
	}
	if err != nil {
		return false, err
	}

	downstreamResource, ok := downstreamObject.(*unstructured.Unstructured)
	if !ok {
		return false, fmt.Errorf("type mismatch of resource object: received %T", downstreamResource)
	}
	resourceVersionDownstream := downstreamResource.GetResourceVersion()
	markedForDeletionDownstream := downstreamResource.GetDeletionTimestamp() != nil

	if upstreamObject == nil && !markedForDeletionDownstream {
		// Resource doesn't exist upstream => let's create it upstream unless the corresponding downstream resource has been marked for deletion.
		logger.Info("Creating resource upstream")
		preparedResource := c.prepareResourceForUpstream(ctx, gvr, upstreamNamespace, upstreamClusterName, downstreamResource)

		if !dirtyStatus {
			// if no status needs to be upsynced upstream, then we can set the resource version annotation at the same time as we create the
			// resource
			preparedResource.SetAnnotations(addResourceVersionAnnotation(resourceVersionDownstream, preparedResource.GetAnnotations()))
			// Create the resource
			_, err := c.upstreamClient.Resource(gvr).Cluster(upstreamClusterName.Path()).Namespace(upstreamNamespace).Create(ctx, preparedResource, metav1.CreateOptions{})
			return false, err
		}

		// Status also needs to be upsynced so let's do it in 3 steps:
		// - create the resource
		createdResource, err := c.upstreamClient.Resource(gvr).Cluster(upstreamClusterName.Path()).Namespace(upstreamNamespace).Create(ctx, preparedResource, metav1.CreateOptions{})
		if err != nil {
			return false, err
		}
		// - update the status as a distinct action,
		preparedResource.SetResourceVersion(createdResource.GetResourceVersion())
		updatedResource, err := c.upstreamClient.Resource(gvr).Cluster(upstreamClusterName.Path()).Namespace(upstreamNamespace).UpdateStatus(ctx, preparedResource, metav1.UpdateOptions{})
		if err != nil {
			return false, err
		}
		// - finally update the main content again to set the resource version annotation to the value of the downstream resource version
		preparedResource.SetAnnotations(addResourceVersionAnnotation(resourceVersionDownstream, preparedResource.GetAnnotations()))
		preparedResource.SetResourceVersion(updatedResource.GetResourceVersion())
		_, err = c.upstreamClient.Resource(gvr).Cluster(upstreamClusterName.Path()).Namespace(upstreamNamespace).Update(ctx, preparedResource, metav1.UpdateOptions{})
		return false, err
	}

	resourceVersionUpstream := upstreamObject.GetAnnotations()[ResourceVersionAnnotation]
	if resourceVersionDownstream != resourceVersionUpstream {
		// Resource version annotation on the upstream resource doesn't match the downstream resource version
		// => let's update the resource upstream.
		logger.Info("Updating upstream resource")
		preparedResource := c.prepareResourceForUpstream(ctx, gvr, upstreamNamespace, upstreamClusterName, downstreamResource)
		if err != nil {
			return false, err
		}

		if !dirtyStatus {
			// if no status needs to be upsynced upstream, then we can set the resource version annotation to the downstream resource version
			// at the same time as we update the main resource content.

			preparedResource.SetAnnotations(addResourceVersionAnnotation(resourceVersionDownstream, preparedResource.GetAnnotations()))
			existingResource, err := c.upstreamClient.Resource(gvr).Cluster(upstreamClusterName.Path()).Namespace(upstreamNamespace).Get(ctx, preparedResource.GetName(), metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			preparedResource.SetResourceVersion(existingResource.GetResourceVersion())
			_, err = c.upstreamClient.Resource(gvr).Cluster(upstreamClusterName.Path()).Namespace(upstreamNamespace).Update(ctx, preparedResource, metav1.UpdateOptions{})
			// If the downstream resource is marked for deletion, let's requeue it to manage the deletion timestamp
			return markedForDeletionDownstream, err
		}

		// Status also needs to be upsynced so let's do it in 3 steps:
		// - update the upstream resource main content

		preparedResource.SetAnnotations(addResourceVersionAnnotation(resourceVersionUpstream, preparedResource.GetAnnotations()))
		existingResource, err := c.upstreamClient.Resource(gvr).Cluster(upstreamClusterName.Path()).Namespace(upstreamNamespace).Get(ctx, preparedResource.GetName(), metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		preparedResource.SetResourceVersion(existingResource.GetResourceVersion())
		updatedResource, err := c.upstreamClient.Resource(gvr).Cluster(upstreamClusterName.Path()).Namespace(upstreamNamespace).Update(ctx, preparedResource, metav1.UpdateOptions{})
		if err != nil {
			return false, err
		}

		// - update the status as a distinct action,
		preparedResource.SetResourceVersion(updatedResource.GetResourceVersion())
		updatedResource, err = c.upstreamClient.Resource(gvr).Cluster(upstreamClusterName.Path()).Namespace(upstreamNamespace).UpdateStatus(ctx, preparedResource, metav1.UpdateOptions{})
		if err != nil {
			return false, err
		}

		// - finally update the main content again to set the resource version annotation to the value of the downstream resource version.
		preparedResource.SetAnnotations(addResourceVersionAnnotation(resourceVersionDownstream, preparedResource.GetAnnotations()))
		preparedResource.SetResourceVersion(updatedResource.GetResourceVersion())
		_, err = c.upstreamClient.Resource(gvr).Cluster(upstreamClusterName.Path()).Namespace(upstreamNamespace).Update(ctx, preparedResource, metav1.UpdateOptions{})
		// If the downstream resource is marked for deletion, let's requeue it to manage the deletion timestamp
		return markedForDeletionDownstream, err
	}

	if downstreamResource.GetDeletionTimestamp() != nil {
		return false, c.removeUpstreamResource(ctx, gvr, upstreamClusterName, upstreamNamespace, upstreamName, false)
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

func (c *controller) removeUpstreamResource(ctx context.Context, gvr schema.GroupVersionResource, clusterName logicalcluster.Name, namespace, name string, force bool) error {
	if force {
		existingResource, err := c.upstreamClient.Resource(gvr).Cluster(clusterName.Path()).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if k8serror.IsNotFound(err) {
			return nil
		} else if err != nil {
			return err
		}
		if len(existingResource.GetFinalizers()) > 0 {
			existingResource.SetFinalizers(nil)
			if _, err := c.upstreamClient.Resource(gvr).Cluster(clusterName.Path()).Namespace(namespace).Update(ctx, existingResource, metav1.UpdateOptions{}); k8serror.IsNotFound(err) {
				return nil
			} else if err != nil {
				return err
			}
		}
	}
	if err := c.upstreamClient.Resource(gvr).Cluster(clusterName.Path()).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{}); k8serror.IsNotFound(err) {
		return nil
	} else {
		return err
	}
}

func addResourceVersionAnnotation(resourceVersion string, annotations map[string]string) map[string]string {
	if annotations == nil {
		annotations = make(map[string]string, 1)
	}
	annotations[ResourceVersionAnnotation] = resourceVersion
	return annotations
}
