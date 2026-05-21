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
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apiclient "github.com/kcp-dev/apimachinery/v2/pkg/client"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"
)

// installClientCacheEvictor registers a LogicalCluster delete handler that
// notifies apiclient.EvictCluster, which fans out to every per-cluster client
// cache (kube, sdk, dynamic, metadata, ...) constructed via apiclient.NewCache.
// Without this, those caches grow monotonically and pin per-cluster REST
// clients, codec factories, JSON-decoded schemas, etc. for the lifetime of
// the process. See https://github.com/kcp-dev/kcp/issues/4071.
func installClientCacheEvictor(informer corev1alpha1informers.LogicalClusterClusterInformer) {
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
			klog.V(4).InfoS("evicting per-cluster client caches", "logicalcluster", name)
			apiclient.EvictCluster(name.Path())
		},
	})
}
