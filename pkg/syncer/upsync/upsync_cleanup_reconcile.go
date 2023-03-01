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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	. "github.com/kcp-dev/kcp/tmc/pkg/logging"
)

type cleanupReconciler struct {
	getUpstreamClient func(clusterName logicalcluster.Name) (dynamic.Interface, error)

	getDownstreamLister               func(gvr schema.GroupVersionResource) (cache.GenericLister, error)
	listDownstreamNamespacesByLocator func(jsonLocator string) ([]*unstructured.Unstructured, error)

	syncTargetName        string
	syncTargetClusterName logicalcluster.Name
	syncTargetUID         types.UID
}

func (c *cleanupReconciler) reconcile(ctx context.Context, gvr schema.GroupVersionResource, upstreamClusterName logicalcluster.Name, upstreamNamespace, upstreamName string) (bool, error) {
	downstreamResource, err := c.getDownstreamResource(ctx, gvr, upstreamClusterName, upstreamNamespace, upstreamName)
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	if downstreamResource != nil {
		return false, nil
	}

	// Downstream resource not present => force delete resource upstream (also remove finalizers)
	err = c.deleteOrphanUpstreamResource(ctx, gvr, upstreamClusterName, upstreamNamespace, upstreamName)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return false, err
}

func (c *cleanupReconciler) getDownstreamResource(ctx context.Context, gvr schema.GroupVersionResource, upstreamClusterName logicalcluster.Name, upstreamNamespace, upstreamName string) (*unstructured.Unstructured, error) {
	logger := klog.FromContext(ctx)

	downstreamNamespace := ""
	if upstreamNamespace != "" {
		// find downstream namespace through locator index
		locator := shared.NewNamespaceLocator(upstreamClusterName, c.syncTargetClusterName, c.syncTargetUID, c.syncTargetName, upstreamNamespace)
		locatorValue, err := json.Marshal(locator)
		if err != nil {
			return nil, err
		}
		downstreamNamespaces, err := c.listDownstreamNamespacesByLocator(string(locatorValue))
		if err != nil {
			return nil, err
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
			return nil, fmt.Errorf("(namespace collision) found multiple downstream namespaces: %s for upstream namespace %s|%s", strings.Join(namespacesCollisions, ","), upstreamClusterName, upstreamNamespace)
		} else {
			logger.V(4).Info("No downstream namespaces found")
			return nil, nil
		}
	}

	// retrieve downstream object
	downstreamLister, err := c.getDownstreamLister(gvr)
	if err != nil {
		return nil, err
	}

	var downstreamObject runtime.Object
	if downstreamNamespace != "" {
		downstreamObject, err = downstreamLister.ByNamespace(downstreamNamespace).Get(upstreamName)
	} else {
		downstreamObject, err = downstreamLister.Get(upstreamName)
	}
	if err != nil {
		return nil, err
	}

	downstreamResource, ok := downstreamObject.(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("type mismatch of resource object: received %T", downstreamResource)
	}

	return downstreamResource, nil
}

func removeUpstreamResourceFinalizers(ctx context.Context, upstreamClient dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string) error {
	existingResource, err := upstreamClient.Resource(gvr).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if len(existingResource.GetFinalizers()) > 0 {
		existingResource.SetFinalizers(nil)
		if _, err := upstreamClient.Resource(gvr).Namespace(namespace).Update(ctx, existingResource, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func (c *cleanupReconciler) deleteOrphanUpstreamResource(ctx context.Context, gvr schema.GroupVersionResource, upstreamClusterName logicalcluster.Name, upstreamNamespace, upstreamName string) error {
	// Downstream resource not present => force delete resource upstream (also remove finalizers)
	upstreamClient, err := c.getUpstreamClient(upstreamClusterName)
	if err != nil {
		return err
	}

	if err := removeUpstreamResourceFinalizers(ctx, upstreamClient, gvr, upstreamNamespace, upstreamName); err != nil {
		return err
	}

	err = upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Delete(ctx, upstreamName, metav1.DeleteOptions{})
	return err
}
