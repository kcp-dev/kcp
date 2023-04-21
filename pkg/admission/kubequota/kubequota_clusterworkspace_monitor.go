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

package kubequota

import (
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/logging"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
)

// LogicalClusterDeletionMonitor monitors LogicalClusters and invokes stopFunc for each deleted LogicalCluster.
type LogicalClusterDeletionMonitor struct {
	name     string
	queue    workqueue.RateLimitingInterface
	stopFunc func(name logicalcluster.Name)
}

func NewLogicalClusterDeletionMonitor(
	name string,
	logicalClusterInformer corev1alpha1informers.LogicalClusterClusterInformer,
	stopFunc func(logicalcluster.Name),
) *LogicalClusterDeletionMonitor {
	m := &LogicalClusterDeletionMonitor{
		name:     name,
		queue:    workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), name),
		stopFunc: stopFunc,
	}

	_, _ = logicalClusterInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			m.enqueue(obj)
		},
	})

	return m
}

func (m *LogicalClusterDeletionMonitor) enqueue(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	m.queue.Add(key)
}

func (m *LogicalClusterDeletionMonitor) Start(stop <-chan struct{}) {
	defer runtime.HandleCrash()
	defer m.queue.ShutDown()

	logger := logging.WithReconciler(klog.Background(), m.name)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	go wait.Until(m.startWorker, time.Second, stop)

	<-stop
}

func (m *LogicalClusterDeletionMonitor) startWorker() {
	for m.processNextWorkItem() {
	}
}

func (m *LogicalClusterDeletionMonitor) processNextWorkItem() bool {
	// Wait until there is a new item in the working queue
	k, quit := m.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer m.queue.Done(key)

	if err := m.process(key); err != nil {
		runtime.HandleError(fmt.Errorf("LogicalClusterDeletionMonitor failed to sync %q, err: %w", key, err))

		m.queue.AddRateLimited(key)

		return true
	}

	// Clear rate limiting stats on key
	m.queue.Forget(key)

	return true
}

func (m *LogicalClusterDeletionMonitor) process(key string) error {
	clusterName, _, _, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}

	m.stopFunc(clusterName)

	return nil
}
