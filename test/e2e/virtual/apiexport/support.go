/*
Copyright 2022 The kcp Authors.

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

package apiexport

import (
	"embed"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var testFiles embed.FS

func groupExists(list *metav1.APIGroupList, group string) bool {
	for _, g := range list.Groups {
		if g.Name == group {
			return true
		}
	}
	return false
}

func resourceExists(list *metav1.APIResourceList, resource string) bool {
	for _, r := range list.APIResources {
		if r.Name == resource {
			return true
		}
	}
	return false
}

// vwConfig is a copy of cfg pointing at the APIExport virtual workspace of the
// named export, reachable from the given consumer workspace.
func vwConfig(t *testing.T, cfg *rest.Config, kcpClients kcpclientset.ClusterInterface, consumerWorkspace *tenancyv1alpha1.Workspace, exportPath logicalcluster.Path, exportName string) *rest.Config {
	t.Helper()

	vwCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClients.Cluster(exportPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), exportName, metav1.GetOptions{})
		if kcptestinghelpers.TolerateOrFail(t, err, apierrors.IsNotFound) {
			return false, fmt.Sprintf("waiting on APIExportEndpointSlice to be available %v", err.Error())
		}
		var found bool
		vwCfg.Host, found, err = framework.VirtualWorkspaceURL(t.Context(), cfg, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
		if err != nil {
			return false, fmt.Sprintf("error getting VW URL: %v", err)
		}
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExportEndpointSlice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	return vwCfg
}

// vwResourceClient is a dynamic client for one resource in the consumer
// workspace, as the named export's virtual workspace serves it.
func vwResourceClient(t *testing.T, cfg *rest.Config, kcpClients kcpclientset.ClusterInterface, consumerWorkspace *tenancyv1alpha1.Workspace, exportPath logicalcluster.Path, exportName string, gvr schema.GroupVersionResource) dynamic.ResourceInterface {
	t.Helper()

	vwClient, err := kcpdynamic.NewForConfig(vwConfig(t, cfg, kcpClients, consumerWorkspace, exportPath, exportName))
	require.NoError(t, err)

	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)
	return vwClient.Cluster(consumerClusterName.Path()).Resource(gvr).Namespace("default")
}
