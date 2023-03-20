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

package status

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	workloadcliplugin "github.com/kcp-dev/kcp/pkg/cliplugins/workload/plugin"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
	. "github.com/kcp-dev/kcp/tmc/pkg/logging"
)

func deepEqualFinalizersAndStatus(oldUnstrob, newUnstrob *unstructured.Unstructured) bool {
	newFinalizers := newUnstrob.GetFinalizers()
	oldFinalizers := oldUnstrob.GetFinalizers()

	newStatus := newUnstrob.UnstructuredContent()["status"]
	oldStatus := oldUnstrob.UnstructuredContent()["status"]

	return equality.Semantic.DeepEqual(oldFinalizers, newFinalizers) && equality.Semantic.DeepEqual(oldStatus, newStatus)
}

func (c *Controller) process(ctx context.Context, gvr schema.GroupVersionResource, key string) error {
	logger := klog.FromContext(ctx)

	// from downstream
	downstreamNamespace, downstreamName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		logger.Error(err, "Invalid key")
		return nil
	}
	// TODO(sttts): do not reference the cli plugin here
	if strings.HasPrefix(downstreamNamespace, workloadcliplugin.SyncerIDPrefix) {
		// skip syncer namespace
		return nil
	}

	logger = logger.WithValues(DownstreamNamespace, downstreamNamespace, DownstreamName, downstreamName)

	downstreamLister, err := c.getDownstreamLister(gvr)
	if err != nil {
		return err
	}

	var namespaceLocator *shared.NamespaceLocator
	var locatorExists bool

	if downstreamNamespace != "" {
		downstreamNamespaceLister, err := c.getDownstreamLister(namespaceGVR)
		if err != nil {
			return err
		}

		nsObj, err := downstreamNamespaceLister.Get(downstreamNamespace)
		if err != nil {
			logger.Error(err, "Error retrieving downstream namespace from downstream lister")
			return nil
		}
		nsMeta, ok := nsObj.(metav1.Object)
		if !ok {
			logger.Info(fmt.Sprintf("Error: downstream namespace expected to be metav1.Object, got %T", nsObj))
			return nil
		}

		namespaceLocator, locatorExists, err = shared.LocatorFromAnnotations(nsMeta.GetAnnotations())
		if err != nil {
			logger.Error(err, "Error decoding annotation on downstream namespace")
			return nil
		}
		if !locatorExists || namespaceLocator == nil {
			// Only sync resources for the configured logical cluster to ensure
			// that syncers for multiple logical clusters can coexist.
			return nil
		}
	}

	var resourceExists bool
	obj, err := downstreamLister.ByNamespace(downstreamNamespace).Get(downstreamName)
	if err == nil {
		resourceExists = true
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	if downstreamNamespace == "" {
		if !resourceExists {
			// TODO(davidfestal): The downstream object doesn't exist, but we cannot remove the finalizer
			// on the upstream resource since we dont have the locator to locate the upstream resource.
			// That should be fixed.
			return nil
		}

		objMeta, ok := obj.(metav1.Object)
		if !ok {
			logger.Info(fmt.Sprintf("Error: downstream cluster-wide resource expected to be metav1.Object, got %T", obj))
			return nil
		}
		namespaceLocator, locatorExists, err = shared.LocatorFromAnnotations(objMeta.GetAnnotations())
		if err != nil {
			logger.Error(err, "Error decoding annotation on downstream cluster-wide resource")
			return nil
		}
		if !locatorExists || namespaceLocator == nil {
			// Only sync resources for the configured logical cluster to ensure
			// that syncers for multiple logical clusters can coexist.
			return nil
		}
	}
	if namespaceLocator.SyncTarget.UID != c.syncTargetUID || namespaceLocator.SyncTarget.ClusterName != c.syncTargetWorkspace.String() {
		// not our resource.
		return nil
	}

	upstreamNamespace := namespaceLocator.Namespace
	upstreamClusterName := namespaceLocator.ClusterName
	upstreamName := shared.GetUpstreamResourceName(gvr, downstreamName)

	logger = logger.WithValues(logging.WorkspaceKey, upstreamClusterName, logging.NamespaceKey, upstreamNamespace, logging.NameKey, upstreamName)
	ctx = klog.NewContext(ctx, logger)

	upstreamLister, err := c.getUpstreamLister(upstreamClusterName, gvr)
	if err != nil {
		return err
	}

	upstreamClient, err := c.getUpstreamClient(upstreamClusterName)
	if err != nil {
		return err
	}

	if !resourceExists {
		logger.Info("Downstream object does not exist. Removing finalizer on upstream object")
		return shared.EnsureUpstreamFinalizerRemoved(ctx, gvr, upstreamLister, upstreamClient, upstreamNamespace, c.syncTargetKey, shared.GetUpstreamResourceName(gvr, downstreamName))
	}

	// update upstream status
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("object to synchronize is expected to be Unstructured, but is %T", obj)
	}
	if u.GetLabels()[workloadv1alpha1.ClusterResourceStateLabelPrefix+c.syncTargetKey] == string(workloadv1alpha1.ResourceStateUpsync) {
		logger.V(4).Info("do not update the status in upstream, since the downstream resource is in Upsync mode")
		return nil
	}

	return c.updateStatusInUpstream(ctx, gvr, upstreamClient, upstreamLister, upstreamNamespace, upstreamName, u)
}

func (c *Controller) updateStatusInUpstream(ctx context.Context, gvr schema.GroupVersionResource, upstreamClient dynamic.Interface, upstreamLister cache.GenericLister, upstreamNamespace, upstreamName string, downstreamObj *unstructured.Unstructured) error {
	logger := klog.FromContext(ctx)

	downstreamStatus, statusExists, err := unstructured.NestedFieldCopy(downstreamObj.UnstructuredContent(), "status")
	if err != nil {
		return err
	} else if !statusExists {
		logger.V(5).Info("Downstream resource doesn't contain a status. Skipping updating the status of upstream resource")
		return nil
	}

	existingObj, err := upstreamLister.ByNamespace(upstreamNamespace).Get(upstreamName)
	if err != nil {
		logger.Error(err, "Error getting upstream resource")
		return err
	}

	existing, ok := existingObj.(*unstructured.Unstructured)
	if !ok {
		logger.Info(fmt.Sprintf("Error: Upstream resource expected to be *unstructured.Unstructured, got %T", existing))
		return nil
	}

	newUpstream := existing.DeepCopy()

	if c.advancedSchedulingEnabled {
		statusAnnotationValue, err := json.Marshal(downstreamStatus)
		if err != nil {
			return err
		}
		newUpstreamAnnotations := newUpstream.GetAnnotations()
		if newUpstreamAnnotations == nil {
			newUpstreamAnnotations = make(map[string]string)
		}
		newUpstreamAnnotations[workloadv1alpha1.InternalClusterStatusAnnotationPrefix+c.syncTargetKey] = string(statusAnnotationValue)
		newUpstream.SetAnnotations(newUpstreamAnnotations)

		if reflect.DeepEqual(existing, newUpstream) {
			logger.V(2).Info("No need to update the status annotation of upstream resource")
			return nil
		}

		if upstreamNamespace != "" {
			// In this case we will update the whole resource, not the status, as the status is in the annotation.
			// this is specific to the advancedScheduling flag.
			_, err = upstreamClient.Resource(gvr).Namespace(upstreamNamespace).Update(ctx, newUpstream, metav1.UpdateOptions{})
		} else {
			_, err = upstreamClient.Resource(gvr).Update(ctx, newUpstream, metav1.UpdateOptions{})
		}

		if err != nil {
			logger.Error(err, "Failed updating the status annotation of upstream resource")
			return err
		}
		logger.Info("Updated the status annotation of upstream resource")
		return nil
	}

	if err := unstructured.SetNestedField(newUpstream.UnstructuredContent(), downstreamStatus, "status"); err != nil {
		logger.Error(err, "Failed setting status of upstream resource")
		return err
	}

	// TODO (davidfestal): Here in the future we might want to also set some fields of the Spec, per resource type, for example:
	// clusterIP for service, or other field values set by SyncTarget cluster admission.
	// But for now let's only update the status.
	if upstreamNamespace != "" {
		_, err = upstreamClient.Resource(gvr).Namespace(upstreamNamespace).UpdateStatus(ctx, newUpstream, metav1.UpdateOptions{})
	} else {
		_, err = upstreamClient.Resource(gvr).UpdateStatus(ctx, newUpstream, metav1.UpdateOptions{})
	}
	if err != nil {
		logger.Error(err, "Failed updating status of upstream resource")
		return err
	}

	logger.Info("Updated status of upstream resource")
	return nil
}
