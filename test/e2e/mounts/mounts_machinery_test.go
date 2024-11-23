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

	corev1 "k8s.io/api/core/v1"
	crdhelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var testFiles embed.FS

func TestMountsMachinery(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	tokenAuthFile := framework.WriteTokenAuthFile(t)
	serverArgs := framework.TestServerArgsWithTokenAuthFile(tokenAuthFile)
	serverArgs = append(serverArgs, "--feature-gates=WorkspaceMounts=true")

	server := framework.PrivateKcpServer(t, framework.WithCustomArguments(serverArgs...))

	fmt.Println(server.KubeconfigPath())

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	orgPath, _ := framework.NewOrganizationFixture(t, server)

	workspaceName := "mounts-machinery"
	mountPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName(workspaceName))

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

	t.Logf("waiting for CRD to be established")
	var lastMsg string
	name := "kubeclusters.contrib.kcp.io"
	err = wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		crd, err := extensionsClusterClient.Cluster(orgPath).CustomResourceDefinitions().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, fmt.Errorf("CRD %s was deleted before being established", name)
			}
			return false, fmt.Errorf("error fetching CRD %s: %w", name, err)
		}
		var reason string
		condition := crdhelpers.FindCRDCondition(crd, apiextensionsv1.Established)
		if condition == nil {
			reason = fmt.Sprintf("CRD has no %s condition", apiextensionsv1.Established)
		} else {
			reason = fmt.Sprintf("CRD is not established: %s: %s", condition.Reason, condition.Message)
		}
		if reason != lastMsg {
			lastMsg = reason
		}
		t.Log(reason)
		return crdhelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established), nil
	})
	require.NoError(t, err)

	t.Logf("Install a mount object into workspace %q", orgPath)
	mapper2 := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(orgProviderKCPClient.Cluster(orgPath).Discovery()))
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(orgPath), mapper2, nil, "kubecluster_mounts.yaml", testFiles)
	require.NoError(t, err)

	// At this point we have object backing the mount object. So lets add mount annotation to the workspace.
	t.Logf("Install a mount annotation into workspace %q", orgPath)
	current, err := kcpClusterClient.Cluster(orgPath).TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
	require.NoError(t, err)

	mount := tenancyv1alpha1.Mount{
		MountSpec: tenancyv1alpha1.MountSpec{
			Reference: &corev1.ObjectReference{
				APIVersion: "contrib.kcp.io/v1alpha1",
				Kind:       "KubeCluster",
				Name:       "proxy-cluster", // must match name in kubecluster_mounts.yaml
			},
		},
	}
	data, err := json.Marshal(mount)
	require.NoError(t, err)

	current.Annotations[tenancyv1alpha1.ExperimentalWorkspaceMountAnnotationKey] = string(data)

	_, err = kcpClusterClient.Cluster(orgPath).TenancyV1alpha1().Workspaces().Update(ctx, current, metav1.UpdateOptions{})
	require.NoError(t, err)

	mountGVR := schema.GroupVersionResource{
		Group:    "contrib.kcp.io",
		Version:  "v1alpha1",
		Resource: "kubeclusters",
	}
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

	_, err = dynamicClusterClient.Cluster(orgPath).Resource(mountGVR).Namespace("").UpdateStatus(ctx, currentMount, metav1.UpdateOptions{})
	require.NoError(t, err)

	// Workspace should have 2 conditions now, and be in Ready state
	err = wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		current, err := kcpClusterClient.Cluster(orgPath).TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		if len(current.Status.Conditions) != 2 {
			return false, nil
		}
		var failed bool
		for _, condition := range current.Status.Conditions {
			if condition.Status != corev1.ConditionTrue {
				failed = true
			}
		}
		return !failed, nil
	})
	require.NoError(t, err)

	// Workspace access should work.
	_, err = kcpClusterClient.Cluster(mountPath).ApisV1alpha1().APIExports().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	// set mount to not ready and verify workspace is not ready
	currentMount, err = dynamicClusterClient.Cluster(orgPath).Resource(mountGVR).Namespace("").Get(ctx, "proxy-cluster", metav1.GetOptions{})
	require.NoError(t, err)
	currentMount.Object["status"].(map[string]interface{})["phase"] = "Connecting"
	_, err = dynamicClusterClient.Cluster(orgPath).Resource(mountGVR).Namespace("").UpdateStatus(ctx, currentMount, metav1.UpdateOptions{})
	require.NoError(t, err)

	err = wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		current, err := kcpClusterClient.Cluster(orgPath).TenancyV1alpha1().Workspaces().Get(ctx, workspaceName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return current.Status.Phase == corev1alpha1.LogicalClusterPhaseUnavailable, nil
	})
	require.NoError(t, err)

	// Workspace access should not with denied.
	err = wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		_, err = kcpClusterClient.Cluster(mountPath).ApisV1alpha1().APIExports().List(ctx, metav1.ListOptions{})
		return err != nil, nil
	})
}
