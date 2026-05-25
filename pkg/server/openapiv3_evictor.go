/*
Copyright 2026 The kcp Authors.

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

package server

import (
	"context"

	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/server/openapiv3"
)

// installOpenAPIV3Evictor registers a LogicalCluster delete handler that drops
// every cached OpenAPI v3 spec for the cluster. The controller's CRD informer
// handler scrubs entries per-CRD, but that path depends on each CRD delete
// event being observed and ordered before the LogicalCluster delete. When that
// ordering breaks the bucket leaks 100KB–1MB per CRD version forever. See
// https://github.com/kcp-dev/kcp/issues/4071.
func installOpenAPIV3Evictor(ctx context.Context, informer corev1alpha1informers.LogicalClusterClusterInformer, controller *openapiv3.Controller) {
	logger := klog.FromContext(ctx).WithName("openapiv3-evictor")
	_, _ = informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj any) {
			lc, ok := obj.(*corev1alpha1.LogicalCluster)
			if !ok {
				tombstone, tok := obj.(cache.DeletedFinalStateUnknown)
				if !tok {
					return
				}
				lc, ok = tombstone.Obj.(*corev1alpha1.LogicalCluster)
				if !ok {
					return
				}
			}
			name := logicalcluster.From(lc)
			if name == "" {
				return
			}
			logger.V(4).Info("evicting OpenAPI v3 cache", "logicalcluster", name)
			controller.EvictCluster(name)
		},
	})
}
