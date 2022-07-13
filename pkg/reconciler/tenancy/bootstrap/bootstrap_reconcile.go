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

package bootstrap

import (
	"context"
	"time"

	"github.com/kcp-dev/logicalcluster"

	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func (c *controller) reconcile(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) error {
	if workspace.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseInitializing {
		return nil
	}
	if workspace.Spec.Type != c.workspaceType {
		return nil
	}

	// have we done our work before?
	initializerName := initialization.InitializerForReference(c.workspaceType)
	if !initialization.InitializerPresent(initializerName, workspace.Status.Initializers) {
		return nil
	}

	// bootstrap resources
	wsClusterName := logicalcluster.From(workspace).Join(workspace.Name)
	klog.Infof("Bootstrapping resources for org workspace %s, logical cluster %s", workspace.Name, wsClusterName)
	bootstrapCtx, cancel := context.WithDeadline(ctx, time.Now().Add(time.Second*30)) // to not block the controller
	defer cancel()
	if err := c.bootstrap(bootstrapCtx, c.crdClient.Cluster(wsClusterName).Discovery(), c.dynamicClient.Cluster(wsClusterName), c.kcpClient, wsClusterName); err != nil {
		return err // requeue
	}

	// we are done. remove our initializer
	workspace.Status.Initializers = initialization.EnsureInitializerAbsent(initializerName, workspace.Status.Initializers)

	return nil
}
