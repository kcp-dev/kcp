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

package authorizer

import (
	"context"
	"embed"
	"testing"
	"time"

	kcpdynamic "github.com/kcp-dev/apimachinery/pkg/dynamic"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/genericcontrolplane"

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcp "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var embeddedResources embed.FS

func TestAuthorizer(t *testing.T) {
	t.Parallel()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	server := framework.SharedKcpServer(t)
	cfg := server.BaseConfig(t)
	rootShardCfg := server.RootShardSystemMasterBaseConfig(t)

	kubeClusterClient, err := kubernetes.NewForConfig(cfg)
	require.NoError(t, err)
	kcpClusterClient, err := kcp.NewForConfig(cfg)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewClusterDynamicClientForConfig(cfg)
	require.NoError(t, err)

	org1 := framework.NewOrganizationFixture(t, server)
	org2 := framework.NewOrganizationFixture(t, server)

	createResources(t, ctx, dynamicClusterClient, kubeClusterClient.DiscoveryClient, org1, "org-resources.yaml")
	createResources(t, ctx, dynamicClusterClient, kubeClusterClient.DiscoveryClient, org2, "org-resources.yaml")

	waitForReady(t, ctx, kcpClusterClient, org1, "workspace1")
	waitForReady(t, ctx, kcpClusterClient, org1, "workspace2")
	waitForReady(t, ctx, kcpClusterClient, org2, "workspace1")
	waitForReady(t, ctx, kcpClusterClient, org2, "workspace2")

	createResources(t, ctx, dynamicClusterClient, kubeClusterClient.DiscoveryClient, org1.Join("workspace1"), "workspace1-resources.yaml")
	createResources(t, ctx, dynamicClusterClient, kubeClusterClient.DiscoveryClient, org2.Join("workspace1"), "workspace1-resources.yaml")

	framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, org1, []string{"user-1", "user-2", "user-3"}, nil, []string{"access"})

	user1KubeClusterClient, err := kubernetes.NewForConfig(framework.UserConfig("user-1", cfg))
	require.NoError(t, err)
	user2KubeClusterClient, err := kubernetes.NewForConfig(framework.UserConfig("user-2", cfg))
	require.NoError(t, err)
	user3KubeClusterClient, err := kubernetes.NewForConfig(framework.UserConfig("user-3", cfg))
	require.NoError(t, err)

	t.Logf("Priming the authorization cache")
	require.Eventually(t, func() bool {
		_, err := user1KubeClusterClient.CoreV1().ConfigMaps("default").List(logicalcluster.WithCluster(ctx, org1.Join("workspace1")), metav1.ListOptions{})
		return err == nil
	}, time.Minute, time.Second)

	tests := map[string]func(t *testing.T){
		"as org member, workspace admin user-1 can access everything": func(t *testing.T) {
			_, err := user1KubeClusterClient.CoreV1().ConfigMaps("default").List(logicalcluster.WithCluster(ctx, org1.Join("workspace1")), metav1.ListOptions{})
			require.NoError(t, err)
			_, err = user1KubeClusterClient.CoreV1().Namespaces().Create(logicalcluster.WithCluster(ctx, org1.Join("workspace1")), &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}}, metav1.CreateOptions{})
			require.NoError(t, err)
			_, err = user1KubeClusterClient.CoreV1().ConfigMaps("test").Create(logicalcluster.WithCluster(ctx, org1.Join("workspace1")), &v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "test"}}, metav1.CreateOptions{})
			require.NoError(t, err)
		},
		"with org access, workspace1 non-admin user-2 can access according to local policy": func(t *testing.T) {
			_, err := user2KubeClusterClient.CoreV1().Namespaces().Create(logicalcluster.WithCluster(ctx, org1.Join("workspace1")), &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}}, metav1.CreateOptions{})
			require.Error(t, err, "user-2 should not be able to create namespace in workspace1")
			_, err = user2KubeClusterClient.CoreV1().Secrets("default").List(logicalcluster.WithCluster(ctx, org1.Join("workspace1")), metav1.ListOptions{})
			require.NoError(t, err, "user-2 should be able to list secrets in workspace1 as defined in the local policy")
		},
		"without org access, org1 workspace1 admin user-1 cannot access org2, not even discovery": func(t *testing.T) {
			_, err := user1KubeClusterClient.CoreV1().ConfigMaps("default").List(logicalcluster.WithCluster(ctx, org2.Join("workspace1")), metav1.ListOptions{})
			require.Error(t, err, "user-1 should not be able to list configmaps in a different org")
			_, err = user1KubeClusterClient.DiscoveryClient.WithCluster(org2.Join("workspace1")).ServerResourcesForGroupVersion("rbac.authorization.k8s.io/v1") // can't be core because that always returns nil
			require.Error(t, err, "user-1 should not be able to list server resources in a different org")
		},
		"as org member, workspace1 admin user-1 cannot access workspace2, not even discovery": func(t *testing.T) {
			_, err := user1KubeClusterClient.CoreV1().ConfigMaps("default").List(logicalcluster.WithCluster(ctx, org1.Join("workspace2")), metav1.ListOptions{})
			require.Error(t, err, "user-1 should not be able to list configmaps in a different workspace")
			_, err = user1KubeClusterClient.DiscoveryClient.WithCluster(org2.Join("workspace1")).ServerResourcesForGroupVersion("rbac.authorization.k8s.io/v1") // can't be core because that always returns nil
			require.Error(t, err, "user-1 should not be able to list server resources in a different workspace")
		},
		"with org access, workspace2 admin user-2 can access workspace2": func(t *testing.T) {
			_, err := user2KubeClusterClient.CoreV1().ConfigMaps("default").List(logicalcluster.WithCluster(ctx, org1.Join("workspace2")), metav1.ListOptions{})
			require.NoError(t, err, "user-2 should be able to list configmaps in workspace2")
		},
		"cluster admins can use wildcard clusters, non-cluster admin cannot": func(t *testing.T) {
			// create client talking directly to root shard to test wildcard requests
			rootKubeClusterClient, err := kubernetes.NewForConfig(rootShardCfg)
			require.NoError(t, err)
			user1RootKubeClusterClient, err := kubernetes.NewForConfig(framework.UserConfig("user-1", rootShardCfg))
			require.NoError(t, err)

			_, err = rootKubeClusterClient.CoreV1().Namespaces().List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
			require.NoError(t, err)
			_, err = user1RootKubeClusterClient.CoreV1().Namespaces().List(logicalcluster.WithCluster(ctx, logicalcluster.Wildcard), metav1.ListOptions{})
			require.Error(t, err, "Only cluster admins can use all clusters at once")
		},
		"with system:admin permissions, workspace2 non-admin user-3 can list Namespaces with a bootstrap ClusterRole": func(t *testing.T) {
			// get workspace2 shard and create a client to tweak the local bootstrap policy
			shardKubeClusterClient, err := kubernetes.NewForConfig(rootShardCfg)
			require.NoError(t, err)

			_, err = user3KubeClusterClient.CoreV1().Namespaces().List(logicalcluster.WithCluster(ctx, org1.Join("workspace2")), metav1.ListOptions{})
			require.Error(t, err, "User-3 shouldn't be able to list Namespaces")

			localAuthorizerClusterRoleBinding := &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "user-3-k8s-ref",
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:     "User",
						APIGroup: "rbac.authorization.k8s.io",
						Name:     "user-3",
					},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     "kcp-authorizer-test-namespace-lister",
				},
			}

			bootstrapClusterRole := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: "kcp-authorizer-test-namespace-lister",
				},
				Rules: []rbacv1.PolicyRule{
					{
						Verbs:     []string{"*"},
						APIGroups: []string{""},
						Resources: []string{"namespaces"},
					},
				},
			}
			_, err = shardKubeClusterClient.RbacV1().ClusterRoleBindings().Create(logicalcluster.WithCluster(ctx, org1.Join("workspace2")), localAuthorizerClusterRoleBinding, metav1.CreateOptions{})
			require.NoError(t, err)

			_, err = shardKubeClusterClient.RbacV1().ClusterRoles().Create(logicalcluster.WithCluster(ctx, genericcontrolplane.LocalAdminCluster), bootstrapClusterRole, metav1.CreateOptions{})
			if err != nil && !errors.IsAlreadyExists(err) {
				require.NoError(t, err)
			}

			require.Eventually(t, func() bool {
				if _, err := user3KubeClusterClient.CoreV1().Namespaces().List(logicalcluster.WithCluster(ctx, org1.Join("workspace2")), metav1.ListOptions{}); err != nil {
					t.Logf("failed to create test namespace: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100, "User-3 should now be able to list Namespaces")
		},
	}

	for tcName, tcFunc := range tests {
		tcName := tcName
		tcFunc := tcFunc
		t.Run(tcName, func(t *testing.T) {
			t.Parallel()
			tcFunc(t)
		})
	}
}

func waitForReady(t *testing.T, ctx context.Context, kcpClusterClient kcp.Interface, orgClusterName logicalcluster.Name, workspace string) {
	t.Logf("Waiting for workspace %s|%s to be ready", orgClusterName, workspace)
	require.Eventually(t, func() bool {
		ws, err := kcpClusterClient.TenancyV1alpha1().ClusterWorkspaces().Get(logicalcluster.WithCluster(ctx, orgClusterName), workspace, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("failed to get workspace %s|%s: %v", orgClusterName, workspace, err)
			return false
		}
		return ws.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady
	}, wait.ForeverTestTimeout, time.Millisecond*100, "workspace %s|%s didn't get ready", orgClusterName, workspace)
}

func createResources(t *testing.T, ctx context.Context, dynamicClusterClient *kcpdynamic.ClusterDynamicClient, discoveryClusterClient *discovery.DiscoveryClient, clusterName logicalcluster.Name, fileName string) {
	t.Logf("Create resources in %s", clusterName)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClusterClient.WithCluster(clusterName)))
	require.Eventually(t, func() bool {
		if err := confighelpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(clusterName), mapper, nil, fileName, embeddedResources); err != nil {
			t.Logf("failed to create resources: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create resources")
}
