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

package replication

import (
	"context"
	"fmt"
	"strings"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

const (
	// ControllerName hold this controller name.
	ControllerName = "kcp-replication-controller"
)

// NewController returns a new replication controller.
//
// The replicated object will be placed under the same cluster as the original object.
// In addition to that, all replicated objects will be placed under the shard taken from the shardName argument.
// For example: shards/{shardName}/clusters/{clusterName}/apis/apis.kcp.io/v1alpha1/apiexports.
func NewController(
	shardName string,
	dynamicCacheClient kcpdynamic.ClusterInterface,
	localKcpInformers kcpinformers.SharedInformerFactory,
	globalKcpInformers kcpinformers.SharedInformerFactory,
	localKubeInformers kcpkubernetesinformers.SharedInformerFactory,
	globalKubeInformers kcpkubernetesinformers.SharedInformerFactory,
) (*controller, error) {
	c := &controller{
		shardName:          shardName,
		queue:              workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName),
		dynamicCacheClient: dynamicCacheClient,

		gvrs: map[schema.GroupVersionResource]replicatedGVR{
			apisv1alpha1.SchemeGroupVersion.WithResource("apiexports"): {
				kind:   "APIExport",
				local:  localKcpInformers.Apis().V1alpha1().APIExports().Informer(),
				global: globalKcpInformers.Apis().V1alpha1().APIExports().Informer(),
			},
			apisv1alpha1.SchemeGroupVersion.WithResource("apiresourceschemas"): {
				kind:   "APIResourceSchema",
				local:  localKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
				global: globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
			},
			apisv1alpha1.SchemeGroupVersion.WithResource("apiconversions"): {
				kind:   "APIConversion",
				local:  localKcpInformers.Apis().V1alpha1().APIConversions().Informer(),
				global: globalKcpInformers.Apis().V1alpha1().APIConversions().Informer(),
			},
			admissionregistrationv1.SchemeGroupVersion.WithResource("mutatingwebhookconfigurations"): {
				kind:   "MutatingWebhookConfiguration",
				local:  localKubeInformers.Admissionregistration().V1().MutatingWebhookConfigurations().Informer(),
				global: globalKubeInformers.Admissionregistration().V1().MutatingWebhookConfigurations().Informer(),
			},
			admissionregistrationv1.SchemeGroupVersion.WithResource("validatingwebhookconfigurations"): {
				kind:   "ValidatingWebhookConfiguration",
				local:  localKubeInformers.Admissionregistration().V1().ValidatingWebhookConfigurations().Informer(),
				global: globalKubeInformers.Admissionregistration().V1().ValidatingWebhookConfigurations().Informer(),
			},
			corev1alpha1.SchemeGroupVersion.WithResource("shards"): {
				kind:   "Shard",
				local:  localKcpInformers.Core().V1alpha1().Shards().Informer(),
				global: globalKcpInformers.Core().V1alpha1().Shards().Informer(),
			},
			corev1alpha1.SchemeGroupVersion.WithResource("logicalclusters"): {
				kind: "LogicalCluster",
				filter: func(u *unstructured.Unstructured) bool {
					return u.GetAnnotations()[core.ReplicateAnnotationKey] != ""
				},
				local:  localKcpInformers.Core().V1alpha1().LogicalClusters().Informer(),
				global: globalKcpInformers.Core().V1alpha1().LogicalClusters().Informer(),
			},
			tenancyv1alpha1.SchemeGroupVersion.WithResource("workspacetypes"): {
				kind:   "WorkspaceType",
				local:  localKcpInformers.Tenancy().V1alpha1().WorkspaceTypes().Informer(),
				global: globalKcpInformers.Tenancy().V1alpha1().WorkspaceTypes().Informer(),
			},
			tenancyv1alpha1.SchemeGroupVersion.WithResource("synctargets"): {
				kind:   "SyncTarget",
				local:  localKcpInformers.Workload().V1alpha1().SyncTargets().Informer(),
				global: globalKcpInformers.Workload().V1alpha1().SyncTargets().Informer(),
			},
			tenancyv1alpha1.SchemeGroupVersion.WithResource("locations"): {
				kind:   "Location",
				local:  localKcpInformers.Scheduling().V1alpha1().Locations().Informer(),
				global: globalKcpInformers.Scheduling().V1alpha1().Locations().Informer(),
			},
			rbacv1.SchemeGroupVersion.WithResource("clusterroles"): {
				kind: "ClusterRole",
				filter: func(u *unstructured.Unstructured) bool {
					return u.GetAnnotations()[core.ReplicateAnnotationKey] != ""
				},
				local:  localKubeInformers.Rbac().V1().ClusterRoles().Informer(),
				global: globalKubeInformers.Rbac().V1().ClusterRoles().Informer(),
			},
			rbacv1.SchemeGroupVersion.WithResource("clusterrolebindings"): {
				kind: "ClusterRoleBinding",
				filter: func(u *unstructured.Unstructured) bool {
					return u.GetAnnotations()[core.ReplicateAnnotationKey] != ""
				},
				local:  localKubeInformers.Rbac().V1().ClusterRoleBindings().Informer(),
				global: globalKubeInformers.Rbac().V1().ClusterRoleBindings().Informer(),
			},
		},
	}

	for gvr, info := range c.gvrs {
		indexers.AddIfNotPresentOrDie(
			info.global.GetIndexer(),
			cache.Indexers{
				ByShardAndLogicalClusterAndNamespaceAndName: IndexByShardAndLogicalClusterAndNamespace,
			},
		)

		// shadow gvr to get the right value in the closure
		gvr := gvr

		info.local.AddEventHandler(cache.FilteringResourceEventHandler{
			FilterFunc: IsNoSystemClusterName,
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc:    func(obj interface{}) { c.enqueueObject(obj, gvr) },
				UpdateFunc: func(_, obj interface{}) { c.enqueueObject(obj, gvr) },
				DeleteFunc: func(obj interface{}) { c.enqueueObject(obj, gvr) },
			},
		})

		info.global.AddEventHandler(cache.FilteringResourceEventHandler{
			FilterFunc: IsNoSystemClusterName, // not really needed, but cannot harm
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc:    func(obj interface{}) { c.enqueueCacheObject(obj, gvr) },
				UpdateFunc: func(_, obj interface{}) { c.enqueueCacheObject(obj, gvr) },
				DeleteFunc: func(obj interface{}) { c.enqueueCacheObject(obj, gvr) },
			},
		})
	}

	return c, nil
}

func (c *controller) enqueueObject(obj interface{}, gvr schema.GroupVersionResource) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	gvrKey := fmt.Sprintf("%s.%s.%s::%s", gvr.Version, gvr.Resource, gvr.Group, key)
	c.queue.Add(gvrKey)
}

func (c *controller) enqueueCacheObject(obj interface{}, gvr schema.GroupVersionResource) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}
	gvrKey := fmt.Sprintf("%s.%s.%s::%s", gvr.Version, gvr.Resource, gvr.Group, key)
	c.queue.Add(gvrKey)
}

// Start starts the controller, which stops when ctx.Done() is closed.
func (c *controller) Start(ctx context.Context, workers int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(cacheclient.WithShardInContext(ctx, shard.New(c.shardName)), logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < workers; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}
	<-ctx.Done()
}

func (c *controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *controller) processNextWorkItem(ctx context.Context) bool {
	grKey, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(grKey)

	logger := logging.WithQueueKey(klog.FromContext(ctx), grKey.(string))
	ctx = klog.NewContext(ctx, logger)
	err := c.reconcile(ctx, grKey.(string))
	if err == nil {
		c.queue.Forget(grKey)
		return true
	}

	runtime.HandleError(fmt.Errorf("%v failed with: %w", grKey, err))
	c.queue.AddRateLimited(grKey)

	return true
}

func IsNoSystemClusterName(obj interface{}) bool {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return false
	}

	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return false
	}
	if strings.HasPrefix(clusterName.String(), "system:") {
		return false
	}
	return true
}

type controller struct {
	shardName string
	queue     workqueue.RateLimitingInterface

	dynamicCacheClient kcpdynamic.ClusterInterface

	gvrs map[schema.GroupVersionResource]replicatedGVR
}

type replicatedGVR struct {
	kind          string
	filter        func(u *unstructured.Unstructured) bool
	global, local cache.SharedIndexInformer
}
