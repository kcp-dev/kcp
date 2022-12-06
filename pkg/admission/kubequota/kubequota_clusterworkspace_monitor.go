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

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	tenancyv1beta1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1beta1"
)

const clusterWorkspaceDeletionMonitorControllerName = "kcp-kubequota-cluster-workspace-deletion-monitor"

// clusterWorkspaceDeletionMonitor monitors ClusterWorkspaces and terminates QuotaAdmission for a logical cluster
// when its corresponding ClusterWorkspace is deleted.
type clusterWorkspaceDeletionMonitor struct {
	queue    workqueue.RateLimitingInterface
	stopFunc func(name logicalcluster.Path)
}

func newClusterWorkspaceDeletionMonitor(
	workspaceInformer tenancyv1beta1informers.WorkspaceClusterInformer,
	stopFunc func(logicalcluster.Path),
) *clusterWorkspaceDeletionMonitor {
	m := &clusterWorkspaceDeletionMonitor{
		queue:    workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), clusterWorkspaceDeletionMonitorControllerName),
		stopFunc: stopFunc,
	}

	workspaceInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			m.enqueue(obj)
		},
	})

	return m
}

func (m *clusterWorkspaceDeletionMonitor) enqueue(obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	m.queue.Add(key)
}

func (m *clusterWorkspaceDeletionMonitor) Start(stop <-chan struct{}) {
	defer runtime.HandleCrash()
	defer m.queue.ShutDown()

	klog.Infof("Starting %s controller", clusterWorkspaceDeletionMonitorControllerName)
	defer klog.Infof("Shutting down %s controller", clusterWorkspaceDeletionMonitorControllerName)

	go wait.Until(m.startWorker, time.Second, stop)

	<-stop
}

func (m *clusterWorkspaceDeletionMonitor) startWorker() {
	for m.processNextWorkItem() {
	}
}

func (m *clusterWorkspaceDeletionMonitor) processNextWorkItem() bool {
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
		runtime.HandleError(fmt.Errorf("clusterWorkspaceDeletionMonitor failed to sync %q, err: %w", key, err))

		m.queue.AddRateLimited(key)

		return true
	}

	// Clear rate limiting stats on key
	m.queue.Forget(key)

	return true
}

func (m *clusterWorkspaceDeletionMonitor) process(key string) error {
	parent, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		runtime.HandleError(err)
		return nil
	}

	// turn it into root:org:ws
	clusterName := parent.Join(name)

	m.stopFunc(clusterName)

	return nil
}
