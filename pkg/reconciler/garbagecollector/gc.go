/*
Copyright 2025 The KCP Authors.

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

package garbagecollector

import (
	"context"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/util/workqueue"

	kcpapiextensionsv1 "github.com/kcp-dev/client-go/apiextensions/informers/apiextensions/v1"
	kcpkubernetesclient "github.com/kcp-dev/client-go/kubernetes"
	kcpmetadataclient "github.com/kcp-dev/client-go/metadata"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/dynamicrestmapper"
)

const NativeControllerName = "kcp-native-garbage-collector"

type Options struct {
	LogicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer
	CRDInformer            kcpapiextensionsv1.CustomResourceDefinitionClusterInformer
	DynRESTMapper          *dynamicrestmapper.DynamicRESTMapper
	Logger                 logr.Logger
	KubeClusterClient      kcpkubernetesclient.ClusterInterface
	MetadataClusterClient  kcpmetadataclient.ClusterInterface
	SharedInformerFactory  *informer.DiscoveringDynamicSharedInformerFactory
	InformersSynced        chan struct{}

	DeletionWorkers int
}

type GarbageCollector struct {
	options Options

	log logr.Logger

	graph *Graph

	handlerCancels map[schema.GroupVersionResource]func()

	deletionQueue workqueue.TypedRateLimitingInterface[*deletionItem]
}

func NewGarbageCollector(options Options) *GarbageCollector {
	gc := &GarbageCollector{}

	gc.options = options
	if gc.options.DeletionWorkers <= 0 {
		gc.options.DeletionWorkers = 2
	}

	gc.log = logging.WithReconciler(options.Logger, NativeControllerName)

	gc.graph = NewGraph()
	gc.handlerCancels = make(map[schema.GroupVersionResource]func())
	gc.deletionQueue = workqueue.NewTypedRateLimitingQueueWithConfig(
		workqueue.DefaultTypedControllerRateLimiter[*deletionItem](),
		workqueue.TypedRateLimitingQueueConfig[*deletionItem]{
			Name: ControllerName,
		},
	)

	return gc
}

func (gc *GarbageCollector) Start(ctx context.Context) {
	defer utilruntime.HandleCrash()
	defer gc.deletionQueue.ShutDown()

	// Wait for informers to be started and synced.
	//
	// TODO(ntnn): Without waiting the GC will fail. Specifically
	// builtin APIs will work and the CRD handlers will register new
	// monitors for new resources _but_ the handlers for these resources
	// then do not fire.
	// That doesn't make a lot of sense to me because registering the
	// handlers and the caches being started and synced should be
	// independent.
	// I suspect that somewhere something in the informer factory is
	// swapped out without carrying the existing regisrations over,
	// causing handlers registered before the swapping to not be
	// notified once the informers are started.
	<-gc.options.InformersSynced

	// Register handlers for builtin APIs and CRDs.
	deregister := gc.registerHandlers(ctx)
	defer deregister()

	// Run deletion workers.
	gc.startDeletion(ctx)

	<-ctx.Done()
}
