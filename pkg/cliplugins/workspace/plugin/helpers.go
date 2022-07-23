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

package plugin

import (
	"context"
	"fmt"
	"path"

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	tenancyclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
)

// getWorkspaceFromInternalName retrieves the workspace with this internal name in the
// user workspace directory, by requesting the `workspaces` virtual workspace.
func getWorkspaceFromInternalName(ctx context.Context, workspaceInternalName string, tenancyClient tenancyclient.Interface) (*tenancyv1beta1.Workspace, error) {
	if list, err := tenancyClient.TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + workspaceInternalName,
	}); err != nil {
		return nil, err
	} else if list == nil || len(list.Items) == 0 {
		return nil, fmt.Errorf("workspace %q is not found", workspaceInternalName)
	} else if len(list.Items) > 1 {
		return nil, fmt.Errorf("several workspaces with the same internal name : %q", workspaceInternalName)
	} else {
		return &list.Items[0], nil
	}
}

type personalClusterClient struct {
	config *rest.Config
}

func (c *personalClusterClient) Cluster(cluster logicalcluster.Name) tenancyclient.Interface {
	virtualConfig := rest.CopyConfig(c.config)
	virtualConfig.Host += path.Join("/services/workspaces", cluster.String(), "personal")
	return tenancyclient.NewForConfigOrDie(virtualConfig)
}
