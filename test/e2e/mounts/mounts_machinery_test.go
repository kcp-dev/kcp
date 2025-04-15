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

package homeworkspaces

import (
	"context"
	"embed"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	crdhelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"

	kcpapiextensionsv1client "github.com/kcp-dev/client-go/apiextensions/client/typed/apiextensions/v1"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var testFiles embed.FS

// TestMountsMachinery is exercising the mounts api. In production we should be configurating front-proxy
// with urls for routing requests to right backend, potentially virtual-workspaces using mappings file.
// This is due to reason front-proxy will not route traffic to not-trusted urls.
// To test this in the text, we will route traffic to another workspace. This kinda simulates mounts in
// symbolic links fashion.

func TestMountsMachinery(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	fmt.Println(server.KubeconfigPath())

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	// This will create structure as bellow for testing:
	// └── root
	//   └── e2e-workspace-n784f
	//    ├── destination
	//    └── source
	//        └── mount
	//
	rootOrg, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path())

	sourcePath, _ := kcptesting.NewWorkspaceFixture(t, server, rootOrg,
		kcptesting.WithName("source"))
	_, destinationWorkspaceObj := kcptesting.NewWorkspaceFixture(t, server, rootOrg,
		kcptesting.WithName("destination"))

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "error creating dynamic cluster client")

	extensionsClusterClient, err := kcpapiextensionsv1client.NewForConfig(cfg)
	require.NoError(t, err, "error creating apiextensions cluster client")

	orgProviderKCPClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating cowboys provider kcp client")

	t.Logf("Install a mount object CRD into workspace %q", sourcePath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(orgProviderKCPClient.Cluster(sourcePath).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(sourcePath), mapper, nil, "crd_kubecluster.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Waiting for CRD to be established")
	name := "kubeclusters.contrib.kcp.io"
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		crd, err := extensionsClusterClient.Cluster(sourcePath).CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
		require.NoError(t, err)
		return crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established), yamlMarshal(t, crd)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CRD to be established")

	t.Logf("Install a mount object into workspace %q", sourcePath)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(orgProviderKCPClient.Cluster(sourcePath).Discovery()))
		err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(sourcePath), mapper, nil, "kubecluster_mounts.yaml", testFiles)
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for mount object to be installed")

	mountWorkspaceName := "mount"
	workspace := tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: mountWorkspaceName,
		},
		Spec: tenancyv1alpha1.WorkspaceSpec{
			Mount: &tenancyv1alpha1.Mount{
				Reference: tenancyv1alpha1.ObjectReference{
					APIVersion: "contrib.kcp.io/v1alpha1",
					Kind:       "KubeCluster",
					Name:       "proxy-cluster",
				},
			},
		},
	}

	t.Logf("Create a workspace %q", sourcePath)
	_, err = kcpClusterClient.Cluster(sourcePath).TenancyV1alpha1().Workspaces().Create(ctx, &workspace, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Updating the mount object to be ready")
	mountGVR := schema.GroupVersionResource{
		Group:    "contrib.kcp.io",
		Version:  "v1alpha1",
		Resource: "kubeclusters",
	}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		destinationWorkspace, err := kcpClusterClient.Cluster(rootOrg).TenancyV1alpha1().Workspaces().Get(ctx, destinationWorkspaceObj.Name, metav1.GetOptions{})
		require.NoError(t, err)

		if destinationWorkspace.Spec.URL == "" {
			return fmt.Errorf("destination workspace %q is not ready", destinationWorkspaceObj.Name)
		}

		currentMount, err := dynamicClusterClient.Cluster(sourcePath).Resource(mountGVR).Namespace("").Get(ctx, "proxy-cluster", metav1.GetOptions{})
		require.NoError(t, err)

		currentMount.Object["status"] = map[string]interface{}{
			"URL": destinationWorkspaceObj.Spec.URL,
			"conditions": []interface{}{
				map[string]interface{}{
					"lastTransitionTime": "2024-10-19T12:30:47Z",
					"message":            "The agent sent a heartbeat",
					"reason":             "AgentReady",
					"status":             "True",
					"type":               "WorkspaceClusterReady",
				},
			},
			"phase": "Ready",
		}

		_, err = dynamicClusterClient.Cluster(sourcePath).Resource(mountGVR).UpdateStatus(ctx, currentMount, metav1.UpdateOptions{})
		return err
	})
	require.NoError(t, err)

	t.Log("Workspace should have WorkspaceMountReady and WorkspaceInitialized conditions, both true")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		current, err := kcpClusterClient.Cluster(sourcePath).TenancyV1alpha1().Workspaces().Get(ctx, mountWorkspaceName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		ready := conditions.IsTrue(current, tenancyv1alpha1.MountConditionReady)
		return ready, yamlMarshal(t, current)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for WorkspaceMountReady condition to be true")

	cluster := logicalcluster.NewPath(sourcePath.String() + ":" + mountWorkspaceName)

	t.Log("Workspace access should work")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(cluster).ApisV1alpha1().APIExports().List(ctx, metav1.ListOptions{})
		return err == nil, fmt.Sprintf("err = %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for workspace access to work")

	t.Log("Set mount to not ready")
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := dynamicClusterClient.Cluster(sourcePath).Resource(mountGVR).Namespace("").Get(ctx, "proxy-cluster", metav1.GetOptions{})
		require.NoError(t, err)
		current.Object["status"].(map[string]interface{})["phase"] = "Connecting"
		updated, err := dynamicClusterClient.Cluster(sourcePath).Resource(mountGVR).Namespace("").UpdateStatus(ctx, current, metav1.UpdateOptions{})
		t.Logf("Updated mount object: %v", yamlMarshal(t, updated))
		return err
	})
	require.NoError(t, err)

	t.Log("Workspace phase should become unavailable")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		current, err := kcpClusterClient.Cluster(sourcePath).TenancyV1alpha1().Workspaces().Get(ctx, mountWorkspaceName, metav1.GetOptions{})
		require.NoError(t, err)
		return current.Status.Phase == corev1alpha1.LogicalClusterPhaseUnavailable, yamlMarshal(t, current)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for workspace to become unavailable")

	t.Logf("Workspace access should eventually fail")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err = kcpClusterClient.Cluster(cluster).ApisV1alpha1().APIExports().List(ctx, metav1.ListOptions{})
		return err != nil, fmt.Sprintf("err = %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for workspace access to fail")
}

func yamlMarshal(t *testing.T, obj interface{}) string {
	data, err := yaml.Marshal(obj)
	require.NoError(t, err)
	return string(data)
}
