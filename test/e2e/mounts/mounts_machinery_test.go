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
	"encoding/json"
	"fmt"
	"testing"
	"time"

	kcpapiextensionsv1client "github.com/kcp-dev/client-go/apiextensions/client/typed/apiextensions/v1"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/stretchr/testify/require"

	crdhelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/config/helpers"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var testFiles embed.FS

func TestMountsMachinery(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	fmt.Println(server.KubeconfigPath())

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	orgPath, _ := framework.NewOrganizationFixture(t, server)

	mountWorkspaceName := "mounts-machinery"
	mountPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("%s", mountWorkspaceName))

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "error creating dynamic cluster client")

	extensionsClusterClient, err := kcpapiextensionsv1client.NewForConfig(cfg)
	require.NoError(t, err, "error creating apiextensions cluster client")

	orgProviderKCPClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating cowboys provider kcp client")

	t.Logf("Install a mount object APIResourceSchema into workspace %q", orgPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(orgProviderKCPClient.Cluster(orgPath).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(orgPath), mapper, nil, "apiresourceschema_kubecluster.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Waiting for CRD to be established")
	name := "kubeclusters.contrib.kcp.io"
	framework.Eventually(t, func() (bool, string) {
		crd, err := extensionsClusterClient.Cluster(orgPath).CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
		require.NoError(t, err)
		return crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established), yamlMarshal(t, crd)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for CRD to be established")

	t.Logf("Install a mount object into workspace %q", orgPath)
	framework.Eventually(t, func() (bool, string) {
		mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(orgProviderKCPClient.Cluster(orgPath).Discovery()))
		err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(orgPath), mapper, nil, "kubecluster_mounts.yaml", testFiles)
		return err == nil, fmt.Sprintf("%v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for mount object to be installed")

	// At this point we have object backing the mount object. So lets add mount annotation to the workspace.
	// But order should not matter.
	t.Logf("Set a mount annotation onto workspace %q", orgPath)
	mount := tenancyv1alpha1.Mount{
		MountSpec: tenancyv1alpha1.MountSpec{
			Reference: &tenancyv1alpha1.ObjectReference{
				APIVersion: "contrib.kcp.io/v1alpha1",
				Kind:       "KubeCluster",
				Name:       "proxy-cluster", // must match name in kubecluster_mounts.yaml
			},
		},
	}
	annValue, err := json.Marshal(mount)
	require.NoError(t, err)
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := kcpClusterClient.Cluster(orgPath).TenancyV1alpha1().Workspaces().Get(ctx, mountWorkspaceName, metav1.GetOptions{})
		require.NoError(t, err)

		current.Annotations[tenancyv1alpha1.ExperimentalWorkspaceMountAnnotationKey] = string(annValue)

		_, err = kcpClusterClient.Cluster(orgPath).TenancyV1alpha1().Workspaces().Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
	require.NoError(t, err)

	t.Log("Updating the mount object to be ready")
	mountGVR := schema.GroupVersionResource{
		Group:    "contrib.kcp.io",
		Version:  "v1alpha1",
		Resource: "kubeclusters",
	}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		currentMount, err := dynamicClusterClient.Cluster(orgPath).Resource(mountGVR).Namespace("").Get(ctx, "proxy-cluster", metav1.GetOptions{})
		require.NoError(t, err)

		currentMount.Object["status"] = map[string]interface{}{
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

		_, err = dynamicClusterClient.Cluster(orgPath).Resource(mountGVR).UpdateStatus(ctx, currentMount, metav1.UpdateOptions{})
		return err
	})
	require.NoError(t, err)

	t.Log("Workspace should have WorkspaceMountReady and WorkspaceInitialized conditions, both true")
	framework.Eventually(t, func() (bool, string) {
		current, err := kcpClusterClient.Cluster(orgPath).TenancyV1alpha1().Workspaces().Get(ctx, mountWorkspaceName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}

		initialized := conditions.IsTrue(current, tenancyv1alpha1.WorkspaceInitialized)
		ready := conditions.IsTrue(current, tenancyv1alpha1.MountConditionReady)
		return initialized && ready, yamlMarshal(t, current)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for WorkspaceMountReady and WorkspaceInitialized conditions to be true")

	t.Log("Workspace access should work")
	framework.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(mountPath).ApisV1alpha1().APIExports().List(ctx, metav1.ListOptions{})
		return err == nil, fmt.Sprintf("err = %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for workspace access to work")

	t.Log("Set mount to not ready")
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := dynamicClusterClient.Cluster(orgPath).Resource(mountGVR).Namespace("").Get(ctx, "proxy-cluster", metav1.GetOptions{})
		require.NoError(t, err)
		current.Object["status"].(map[string]interface{})["phase"] = "Connecting"
		updated, err := dynamicClusterClient.Cluster(orgPath).Resource(mountGVR).Namespace("").UpdateStatus(ctx, current, metav1.UpdateOptions{})
		t.Logf("Updated mount object: %v", yamlMarshal(t, updated))
		return err
	})
	require.NoError(t, err)

	t.Log("Workspace phase should become unavailable")
	framework.Eventually(t, func() (bool, string) {
		current, err := kcpClusterClient.Cluster(orgPath).TenancyV1alpha1().Workspaces().Get(ctx, mountWorkspaceName, metav1.GetOptions{})
		require.NoError(t, err)
		return current.Status.Phase == corev1alpha1.LogicalClusterPhaseUnavailable, yamlMarshal(t, current)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for workspace to become unavailable")

	t.Logf("Workspace access should eventually fail")
	framework.Eventually(t, func() (bool, string) {
		_, err = kcpClusterClient.Cluster(mountPath).ApisV1alpha1().APIExports().List(ctx, metav1.ListOptions{})
		return err != nil, fmt.Sprintf("err = %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting for workspace access to fail")
}

func yamlMarshal(t *testing.T, obj interface{}) string {
	data, err := yaml.Marshal(obj)
	require.NoError(t, err)
	return string(data)
}
