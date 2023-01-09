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

package namespace

import (
	"context"
	"encoding/json"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	. "github.com/kcp-dev/kcp/tmc/pkg/logging"
)

func (c *DownstreamController) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	_, namespaceName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		logger.Error(err, "invalid key")
		return nil
	}

	logger = logger.WithValues(DownstreamNamespace, namespaceName)

	downstreamNamespaceObj, err := c.getDownstreamNamespace(namespaceName)
	if apierrors.IsNotFound(err) {
		logger.V(4).Info("downstream namespace not found, ignoring key")
		return nil
	} else if err != nil {
		logger.Error(err, "failed to get downstream namespace")
		return nil
	}

	downstreamNamespace := downstreamNamespaceObj.(*unstructured.Unstructured)
	logger = logging.WithObject(logger, downstreamNamespace)

	namespaceLocatorJSON := downstreamNamespace.GetAnnotations()[shared.NamespaceLocatorAnnotation]
	if namespaceLocatorJSON == "" {
		logger.Error(nil, "downstream namespace has no namespaceLocator annotation")
		return nil
	}

	nsLocator := shared.NamespaceLocator{}
	if err := json.Unmarshal([]byte(namespaceLocatorJSON), &nsLocator); err != nil {
		logger.Error(err, "failed to unmarshal namespace locator", "namespaceLocator", namespaceLocatorJSON)
		return nil
	}

	// Check if the nsLocator SyncTarget UID is the same as ours. If not, we should ignore this namespace as it could be
	// managed by different syncer.
	if nsLocator.SyncTarget.UID != c.syncTargetUID {
		logger.V(4).Info("downstream namespace is not handled by this sync target, ignoring")
		return nil
	}

	// Always refresh the DNS ConfigMap (even when the namespace has been deleted or is being deleted)
	err = c.updateDNSConfigMap(ctx, nsLocator.ClusterName)
	if err != nil {
		return err
	}

	if !downstreamNamespace.GetDeletionTimestamp().IsZero() {
		logger.V(4).Info("downstream namespace is being deleted, ignoring key")
		return nil
	}

	logger = logger.WithValues(logging.WorkspaceKey, nsLocator.ClusterName, logging.NamespaceKey, nsLocator.Namespace)
	exists, err := c.upstreamNamespaceExists(nsLocator.ClusterName, nsLocator.Namespace)
	if err != nil {
		logger.Error(err, "failed to check if upstream namespace exists")
		return nil
	}
	if !exists {
		logger.Info("adding the downstream namespace to the delayed deletion queue because the upstream namespace doesn't exist")
		c.PlanCleaning(key)
		return nil
	}
	// The namespace exists upstream, so we can remove it from the delayed delete queue
	c.CancelCleaning(key)
	// The upstream namespace still exists, nothing to do.
	return nil
}

func (c *DownstreamController) updateDNSConfigMap(ctx context.Context, clusterName logicalcluster.Name) error {
	logger := klog.FromContext(ctx)
	logger.WithName("dns")
	logger.Info("refreshing logical to physical namespace mapping table")

	// Reconstruct ConfigMap from scratch because:
	// - it's a sound approach
	// - it's low overhead considering operations on namespaces are relatively rare.

	namespaces, err := c.listDownstreamNamespaces()
	if err != nil {
		logger.Error(err, "failed to list downstream namespaces")
		return err // retry
	}

	data := make(map[string]string)
	for _, obj := range namespaces {
		namespace := obj.(*unstructured.Unstructured)
		annotations := namespace.GetAnnotations()
		if annotations == nil {
			// skip
			continue
		}

		locator, found, err := shared.LocatorFromAnnotations(annotations)
		if err != nil {
			// Corrupted ns locator annotation value
			logger.Error(err, "invalid namespace locator", "name", namespace.GetName())
			continue
		}

		if !found {
			continue
		}

		// Only include namespaces in the same workspace
		if locator.ClusterName == clusterName {
			data[locator.Namespace] = namespace.GetName()
		}
	}

	configMapName := shared.GetDNSID(clusterName, c.syncTargetUID, c.syncTargetName)

	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: c.dnsNamespace,
		},
		Data: data,
	}

	// TODO(LV): consider comparing the new ConfigMap with the cached one to avoid a rest api call.

	_, err = c.updateConfigMap(ctx, cm)
	if apierrors.IsNotFound(err) {
		_, err = c.createConfigMap(ctx, cm)
		if err == nil {
			return nil
		}
	}
	if err != nil {
		logger.Error(err, "failed to create or update ConfigMap (retrying)", "name", configMapName, "namespace", c.dnsNamespace)
		return err // retry
	}
	return nil
}
