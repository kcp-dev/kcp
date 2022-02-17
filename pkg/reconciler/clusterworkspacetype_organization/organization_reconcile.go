/*
Copyright 2021 The KCP Authors.

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

package clusterworkspacetype_organization

import (
	"context"
	"time"

	"k8s.io/klog/v2"

	configorganization "github.com/kcp-dev/kcp/config/organization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
)

const (
	OrganizationTypeInitializer = "initializers.tenancy.kcp.dev/organization"
)

func (c *controller) reconcile(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) error {
	if workspace.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseInitializing {
		return nil
	}
	if workspace.Spec.Type != "Organization" {
		return nil
	}

	// have we done our work before?
	found := false
	for _, i := range workspace.Status.Initializers {
		if i == OrganizationTypeInitializer {
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	// bootstrap resources
	wsClusterName := helper.EncodeOrganizationAndWorkspace(workspace.ClusterName, workspace.Name)
	klog.Infof("Bootstrapping resources for org workspace %s, logical cluster %s", workspace.Name, wsClusterName)
	bootstrapCtx, cancel := context.WithDeadline(ctx, time.Now().Add(time.Second*30)) // to not block the controller
	defer cancel()
	if err := configorganization.Bootstrap(bootstrapCtx, c.crdClient.Cluster(wsClusterName), c.dynamicClient.Cluster(wsClusterName)); err != nil {
		return err // requeue
	}

	// we are done. remove our initializer
	newInitializers := make([]tenancyv1alpha1.ClusterWorkspaceInitializer, 0, len(workspace.Status.Initializers))
	for _, i := range workspace.Status.Initializers {
		if i != OrganizationTypeInitializer {
			newInitializers = append(newInitializers, i)
		}
	}
	workspace.Status.Initializers = newInitializers

	return nil
}
