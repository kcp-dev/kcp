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

package endpoints

import (
	"context"
	"strings"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
)

func (c *controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return err
	}
	logger = logger.WithValues(logging.NamespaceKey, namespace, logging.NameKey, name)

	namespaceObj, err := c.getDownstreamNamespace(namespace)
	if kerrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	locator, ok, err := shared.LocatorFromAnnotations(namespaceObj.GetAnnotations())
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	if string(locator.SyncTarget.UID) != string(c.syncTargetUID) ||
		locator.SyncTarget.Name != c.syncTargetName ||
		locator.SyncTarget.ClusterName != c.syncTargetClusterName.String() {
		return nil
	}

	endpoints, err := c.getDownstreamResource(endpointsGVR, namespace, name)
	if kerrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	if endpoints.GetLabels()[workloadv1alpha1.ClusterResourceStateLabelPrefix+c.syncTargetKey] == string(workloadv1alpha1.ResourceStateUpsync) {
		// Endpoints resource already labelled for upsyncing. Nothing more to do.
		return nil
	}

	service, err := c.getDownstreamResource(servicesGVR, namespace, name)
	if kerrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	// Service has owner refs ? => it was certainly created downstream from an already synced higher level resource (KNative ?).
	// Resources synced by the Syncer do not have owner references.
	if len(service.GetOwnerReferences()) > 0 {
		logger.V(3).Info("ignoring endpoint since it has an owner reference")
		return nil
	}

	derivedResourcesToUpsync := strings.Split(service.GetAnnotations()[workloadv1alpha1.ExperimentalUpsyncDerivedResourcesAnnotationKey], ",")
	if len(derivedResourcesToUpsync) == 0 ||
		!sets.NewString(derivedResourcesToUpsync...).Has(endpointsGVR.GroupResource().String()) {
		logger.V(3).Info("ignoring endpoint since it is not mentioned in the service 'workload.kcp.io/upsync-derived-resources' annotation")
		return nil
	}

	logger.V(1).Info("adding the upsync label on endpoint")
	err = c.patchEndpoint(ctx, namespace, name, types.StrategicMergePatchType, []byte(
		`{"metadata": {"labels": {"`+
			workloadv1alpha1.ClusterResourceStateLabelPrefix+c.syncTargetKey+`":"`+string(workloadv1alpha1.ResourceStateUpsync)+
			`"}}}`))
	if kerrors.IsNotFound(err) {
		return nil
	}
	return err
}
