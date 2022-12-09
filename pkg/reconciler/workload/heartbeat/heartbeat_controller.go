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

package heartbeat

import (
	"time"

	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	apiresourcev1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apiresource/v1alpha1"
	workloadv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/basecontroller"
)

const ControllerName = "kcp-cluster-heartbeat"

func NewController(
	kcpClusterClient kcpclientset.ClusterInterface,
	clusterInformer workloadv1alpha1informers.SyncTargetClusterInformer,
	apiResourceImportInformer apiresourcev1alpha1informers.APIResourceImportClusterInformer,
	heartbeatThreshold time.Duration,
) (*basecontroller.ClusterReconciler, error) {
	cm := &clusterManager{
		heartbeatThreshold: heartbeatThreshold,
	}

	r, queue, err := basecontroller.NewClusterReconciler(
		ControllerName,
		cm,
		kcpClusterClient,
		clusterInformer,
		apiResourceImportInformer,
	)
	if err != nil {
		return nil, err
	}
	cm.enqueueClusterAfter = queue.EnqueueAfter
	return r, nil
}
