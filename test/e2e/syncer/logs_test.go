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

package syncer

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	rbachelper "k8s.io/kubernetes/pkg/apis/rbac/v1"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestSyncerLogs(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster:requires-kind")

	if len(framework.TestConfig.PClusterKubeconfig()) == 0 {
		t.Skip("Test requires a pcluster")
	}

	defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.SyncerTunnel, true)()

	upstreamServer := framework.PrivateKcpServer(t)
	t.Log("Creating an organization")
	orgClusterName := framework.NewOrganizationFixture(t, upstreamServer)
	t.Log("Creating a workspace")
	wsClusterName := framework.NewWorkspaceFixture(t, upstreamServer, orgClusterName)

	// The Start method of the fixture will initiate syncer start and then wait for
	// its sync target to go ready. This implicitly validates the syncer
	// heartbeating and the heartbeat controller setting the sync target ready in
	// response.
	syncerFixture := framework.NewSyncerFixture(t, upstreamServer, wsClusterName, framework.WithExtraResources("pods")).Start(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	t.Logf("Bind location workspace")
	framework.NewBindCompute(t, wsClusterName, upstreamServer).Bind(t)

	upstreamConfig := upstreamServer.BaseConfig(t)
	upstreamKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(upstreamConfig)
	require.NoError(t, err)

	t.Log("Creating upstream namespace...")
	upstreamNamespace, err := upstreamKubeClusterClient.Cluster(wsClusterName).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-syncer",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	downstreamKubeClient, err := kubernetesclientset.NewForConfig(syncerFixture.DownstreamConfig)
	require.NoError(t, err)

	upstreamKcpClient, err := kcpclientset.NewForConfig(syncerFixture.SyncerConfig.UpstreamConfig)
	require.NoError(t, err)

	syncTarget, err := upstreamKcpClient.Cluster(wsClusterName).WorkloadV1alpha1().SyncTargets().Get(ctx,
		syncerFixture.SyncerConfig.SyncTargetName,
		metav1.GetOptions{},
	)
	require.NoError(t, err)

	desiredNSLocator := shared.NewNamespaceLocator(wsClusterName, logicalcluster.From(syncTarget),
		syncTarget.GetUID(), syncTarget.Name, upstreamNamespace.Name)
	downstreamNamespaceName, err := shared.PhysicalClusterNamespaceName(desiredNSLocator)
	require.NoError(t, err)

	t.Logf("Waiting for downstream namespace to be created...")
	require.Eventually(t, func() bool {
		_, err = downstreamKubeClient.CoreV1().Namespaces().Get(ctx, downstreamNamespaceName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false
			}
			require.NoError(t, err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "downstream namespace %s for upstream namespace %s was not created", downstreamNamespaceName, upstreamNamespace.Name)

	configMapName := "kcp-root-ca.crt"
	t.Logf("Waiting for downstream configmap %s/%s to be created...", downstreamNamespaceName, configMapName)
	require.Eventually(t, func() bool {
		_, err = downstreamKubeClient.CoreV1().ConfigMaps(downstreamNamespaceName).Get(ctx, configMapName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false
		}
		if err != nil {
			t.Errorf("saw an error waiting for downstream configmap %s/%s to be created: %v", downstreamNamespaceName, configMapName, err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "downstream configmap %s/%s was not created", downstreamNamespaceName, configMapName)

	t.Logf("Create service account permissions for pods access to the downstream syncer user")
	podsAllRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pods-all"},
		Rules: []rbacv1.PolicyRule{
			rbachelper.NewRule("*").Groups("").Resources("pods").RuleOrDie(),
			rbachelper.NewRule("*").Groups("").Resources("pods/log").RuleOrDie(),
			rbachelper.NewRule("*").Groups("").Resources("pods/exec").RuleOrDie(),
		},
	}

	_, err = downstreamKubeClient.RbacV1().ClusterRoles().Create(ctx, podsAllRole, metav1.CreateOptions{})
	if err != nil {
		require.NoError(t, err, "failed to create downstream role")
	}
	//nolint:errcheck
	defer downstreamKubeClient.RbacV1().ClusterRoles().Delete(context.TODO(), podsAllRole.Name, metav1.DeleteOptions{})

	podsAllRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pods-all"},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: syncerFixture.SyncerID, Namespace: syncerFixture.SyncerID},
		},
		RoleRef: rbacv1.RoleRef{Kind: "ClusterRole", Name: "test-pods-all"},
	}

	_, err = downstreamKubeClient.RbacV1().ClusterRoleBindings().Create(ctx, podsAllRoleBinding, metav1.CreateOptions{})
	if err != nil {
		require.NoError(t, err, "failed to create downstream rolebinding")
	}
	//nolint:errcheck
	defer downstreamKubeClient.RbacV1().ClusterRoleBindings().Delete(context.TODO(), podsAllRoleBinding.Name, metav1.DeleteOptions{})

	t.Log("Creating upstream Deployment ...")
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tunnel-test",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"foo": "bar"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"foo": "bar"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    "busybox",
							Image:   "ghcr.io/distroless/busybox:1.35.0-r23",
							Command: []string{"/bin/sh", "-c", `date; for i in $(seq 1 3000); do echo "$(date) Try: ${i}"; sleep 1; done`},
						},
					},
				},
			},
		},
	}

	_, err = upstreamKubeClusterClient.Cluster(wsClusterName).AppsV1().Deployments(upstreamNamespace.Name).Create(ctx, d, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Waiting for downstream Deployment to be ready ...")
	framework.Eventually(t, func() (bool, string) {
		deployment, err := downstreamKubeClient.AppsV1().Deployments(downstreamNamespaceName).Get(ctx, d.Name, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		if deployment.Status.ReadyReplicas != 1 {
			return false, fmt.Sprintf("expected 1 ready replica, got %d", deployment.Status.ReadyReplicas)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Log("Upsyncing downstream POD to KCP")

	// Get the downstream deployment POD name
	pods, err := downstreamKubeClient.CoreV1().Pods(downstreamNamespaceName).List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, pods.Items, 1)

	// Upsync the downstream deployment POD to KCP
	pod := pods.Items[0]
	pod.ObjectMeta.GenerateName = ""
	pod.Namespace = upstreamNamespace.Name
	pod.ResourceVersion = ""
	pod.OwnerReferences = nil

	labels := pod.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["state.workload.kcp.dev/"+workloadv1alpha1.ToSyncTargetKey(logicalcluster.From(syncTarget), syncTarget.Name)] = "Upsync"
	pod.SetLabels(labels)

	// Create a client that uses the upsyncer URL
	upsyncerKCPClient, err := kcpkubernetesclientset.NewForConfig(syncerFixture.UpsyncerVirtualWorkspaceConfig)
	require.NoError(t, err)

	_, err = upsyncerKCPClient.Cluster(wsClusterName).CoreV1().Pods(upstreamNamespace.Name).Create(ctx, &pod, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Getting Pod logs from upstream cluster %q as a normal kubectl client would do.", wsClusterName)
	framework.Eventually(t, func() (bool, string) {
		pods, err := upstreamKubeClusterClient.Cluster(wsClusterName).CoreV1().Pods(upstreamNamespace.Name).List(ctx, metav1.ListOptions{})
		if apierrors.IsUnauthorized(err) {
			return false, fmt.Sprintf("failed to list pods: %v", err)
		}
		require.NoError(t, err)

		var podLogs bytes.Buffer
		for _, pod := range pods.Items {
			request := upstreamKubeClusterClient.Cluster(wsClusterName).CoreV1().Pods(upstreamNamespace.Name).GetLogs(pod.Name, &corev1.PodLogOptions{})
			logs, err := request.Do(ctx).Raw()
			if err != nil {
				return false, err.Error()
			}
			podLogs.Write(logs)
		}

		return podLogs.Len() > 1, podLogs.String()
	}, wait.ForeverTestTimeout, time.Millisecond*100, "couldn't get downstream pod logs %s/%s", pod.Namespace, pod.Name)
}
