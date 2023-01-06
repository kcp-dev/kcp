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
	"encoding/json"
	"testing"
	"time"

	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	authorizationv1 "k8s.io/api/authorization/v1"
	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	apimachineryversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/kubernetes/pkg/genericcontrolplane"

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var embeddedResources embed.FS

func TestAuthorizer(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	server := framework.SharedKcpServer(t)
	cfg := server.BaseConfig(t)
	rootShardCfg := server.RootShardSystemMasterBaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kubeDiscoveryClient, err := kcpdiscovery.NewForConfig(cfg)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)

	org1 := framework.NewOrganizationFixture(t, server)
	org2 := framework.NewOrganizationFixture(t, server, framework.WithRequiredGroups("empty-group"))

	framework.NewWorkspaceFixture(t, server, org1.Path(), framework.WithName("workspace1"))
	framework.NewWorkspaceFixture(t, server, org1.Path(), framework.WithName("workspace2"))
	framework.NewWorkspaceFixture(t, server, org2.Path(), framework.WithName("workspace1"), framework.WithRootShard()) // on root for deep SAR test
	framework.NewWorkspaceFixture(t, server, org2.Path(), framework.WithName("workspace2"))

	createResources(ctx, t, dynamicClusterClient, kubeDiscoveryClient, org1.Path().Join("workspace1"), "workspace1-resources.yaml")
	createResources(ctx, t, dynamicClusterClient, kubeDiscoveryClient, org2.Path().Join("workspace1"), "workspace1-resources.yaml")

	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, org1.Path(), []string{"user-1", "user-2", "user-3"}, nil, false)

	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, org1.Path().Join("workspace1"), []string{"user-1"}, nil, true)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, org1.Path().Join("workspace1"), []string{"user-2"}, nil, false)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, org1.Path().Join("workspace2"), []string{"user-3"}, nil, false)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, org1.Path().Join("workspace2"), []string{"user-2"}, nil, true)

	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, org2.Path().Join("workspace1"), []string{"user-1"}, nil, true)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, org2.Path().Join("workspace1"), []string{"user-2"}, nil, false)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, org2.Path().Join("workspace2"), []string{"user-3"}, nil, false)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, org2.Path().Join("workspace2"), []string{"user-2"}, nil, true)

	user1KubeClusterClient, err := kcpkubernetesclientset.NewForConfig(framework.UserConfig("user-1", cfg))
	require.NoError(t, err)
	user1KubeDiscoveryClient, err := kcpdiscovery.NewForConfig(framework.UserConfig("user-1", cfg))
	require.NoError(t, err)
	user2KubeClusterClient, err := kcpkubernetesclientset.NewForConfig(framework.UserConfig("user-2", cfg))
	require.NoError(t, err)
	user3KubeClusterClient, err := kcpkubernetesclientset.NewForConfig(framework.UserConfig("user-3", cfg))
	require.NoError(t, err)

	t.Logf("Priming the authorization cache")
	require.Eventually(t, func() bool {
		_, err := user1KubeClusterClient.Cluster(org1.Path().Join("workspace1")).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
		return err == nil
	}, 2*wait.ForeverTestTimeout, 100*time.Millisecond)

	tests := map[string]func(t *testing.T){
		"as org member, workspace admin user-1 can access everything": func(t *testing.T) {
			t.Helper()
			_, err := user1KubeClusterClient.Cluster(org1.Path().Join("workspace1")).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
			require.NoError(t, err)
			_, err = user1KubeClusterClient.Cluster(org1.Path().Join("workspace1")).CoreV1().Namespaces().Create(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}}, metav1.CreateOptions{})
			require.NoError(t, err)
			_, err = user1KubeClusterClient.Cluster(org1.Path().Join("workspace1")).CoreV1().ConfigMaps("test").Create(ctx, &v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "test"}}, metav1.CreateOptions{})
			require.NoError(t, err)
		},
		"with org access, workspace1 non-admin user-2 can access according to local policy": func(t *testing.T) {
			t.Helper()
			_, err := user2KubeClusterClient.Cluster(org1.Path().Join("workspace1")).CoreV1().Namespaces().Create(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test"}}, metav1.CreateOptions{})
			require.Errorf(t, err, "user-2 should not be able to create namespace in %s", org1.Path().Join("workspace1"))
			_, err = user2KubeClusterClient.Cluster(org1.Path().Join("workspace1")).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
			require.NoErrorf(t, err, "user-2 should be able to list secrets in %s as defined in the local policy", org1.Path().Join("workspace1"))
		},
		"with org access, workspace1 non-admin user-2 can access /healthz, /livez, /readyz etc": func(t *testing.T) {
			t.Helper()
			cl := user2KubeClusterClient.RESTClient()
			requestPath := org1.Path().RequestPath()
			{
				for endpoint := range sets.NewString("/healthz", "/readyz", "/livez") {
					req := cl.Get().AbsPath(requestPath + endpoint)
					respBytes, err := req.DoRaw(ctx)
					require.NoError(t, err, "should be able to GET %s", endpoint)
					require.Equal(t, "ok", string(respBytes))
				}
			}
			{
				endpoint := "/version"
				req := cl.Get().AbsPath(requestPath + endpoint)
				respBytes, err := req.DoRaw(ctx)
				require.NoError(t, err, "should be able to GET %s", endpoint)
				require.NotEmpty(t, string(respBytes))
				version := new(apimachineryversion.Info)
				require.NoError(t, json.Unmarshal(respBytes, version))
			}
		},
		"without org access, org1 workspace1 admin user-1 cannot access org2, not even discovery": func(t *testing.T) {
			t.Helper()
			_, err := user1KubeClusterClient.Cluster(org2.Path().Join("workspace1")).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
			require.Errorf(t, err, "user-1 should not be able to list configmaps in a different org (%s)", org2.Path().Join("workspace1"))
			_, err = user1KubeDiscoveryClient.Cluster(org2.Path().Join("workspace1")).ServerResourcesForGroupVersion("rbac.authorization.k8s.io/v1") // can't be core because that always returns nil
			require.Errorf(t, err, "user-1 should not be able to list server resources in a different org (%s)", org2.Path().Join("workspace1"))
		},
		"as org member, workspace1 admin user-1 cannot access workspace2, not even discovery": func(t *testing.T) {
			t.Helper()
			_, err := user1KubeClusterClient.Cluster(org1.Path().Join("workspace2")).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
			require.Errorf(t, err, "user-1 should not be able to list configmaps in a different workspace (%s)", org1.Path().Join("workspace2"))
			_, err = user1KubeDiscoveryClient.Cluster(org2.Path().Join("workspace1")).ServerResourcesForGroupVersion("rbac.authorization.k8s.io/v1") // can't be core because that always returns nil
			require.Errorf(t, err, "user-1 should not be able to list server resources in a different workspace (%s)", org1.Path().Join("workspace2"))
		},
		"with org access, workspace2 admin user-2 can access workspace2": func(t *testing.T) {
			t.Helper()
			_, err := user2KubeClusterClient.Cluster(org1.Path().Join("workspace2")).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
			require.NoError(t, err, "user-2 should be able to list configmaps in workspace2 (%s)", org1.Path().Join("workspace2"))
		},
		"cluster admins can use wildcard clusters, non-cluster admin cannot": func(t *testing.T) {
			t.Helper()
			// create client talking directly to root shard to test wildcard requests
			rootKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(rootShardCfg)
			require.NoError(t, err)
			user1RootKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(framework.UserConfig("user-1", rootShardCfg))
			require.NoError(t, err)

			_, err = rootKubeClusterClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
			require.NoError(t, err)
			_, err = user1RootKubeClusterClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
			require.Error(t, err, "Only cluster admins can use all clusters at once")
		},
		"with system:admin permissions, workspace2 non-admin user-3 can list Namespaces with a bootstrap ClusterRole": func(t *testing.T) {
			t.Helper()
			// get workspace2 shard and create a client to tweak the local bootstrap policy
			shardKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(rootShardCfg)
			require.NoError(t, err)

			_, err = user3KubeClusterClient.Cluster(org1.Path().Join("workspace2")).CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
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
			_, err = shardKubeClusterClient.Cluster(org1.Path().Join("workspace2")).RbacV1().ClusterRoleBindings().Create(ctx, localAuthorizerClusterRoleBinding, metav1.CreateOptions{})
			require.NoError(t, err)

			_, err = shardKubeClusterClient.Cluster(genericcontrolplane.LocalAdminCluster.Path()).RbacV1().ClusterRoles().Create(ctx, bootstrapClusterRole, metav1.CreateOptions{})
			if err != nil && !errors.IsAlreadyExists(err) {
				require.NoError(t, err)
			}

			require.Eventually(t, func() bool {
				if _, err := user3KubeClusterClient.Cluster(org1.Path().Join("workspace2")).CoreV1().Namespaces().List(ctx, metav1.ListOptions{}); err != nil {
					t.Logf("failed to create test namespace: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100, "User-3 should now be able to list Namespaces in %s", org1.Path().Join("workspace2"))
		},
		"without org access, a deep SAR with user-1 against org2 succeeds even without org access for user-1": func(t *testing.T) {
			t.Helper()
			t.Logf("try to list ConfigMap as user-1 in %q without access, should fail", org2.Path().Join("workspace1"))
			_, err := user1KubeClusterClient.Cluster(org2.Path().Join("workspace1")).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
			require.Errorf(t, err, "user-1 should not be able to list configmaps in %q", org2.Path().Join("workspace1"))

			sar := &authorizationv1.SubjectAccessReview{
				Spec: authorizationv1.SubjectAccessReviewSpec{
					ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: "default", Verb: "list", Version: "v1", Resource: "configmaps"},
					User:               "user-1",
					Groups:             []string{"team-1"},
				},
			}

			t.Logf("ask with normal SAR that user-1 cannot access %q because it has no access", org2.Path().Join("workspace1"))
			resp, err := kubeClusterClient.Cluster(org2.Path().Join("workspace1")).AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
			require.NoError(t, err)
			require.Equalf(t, "access denied", resp.Status.Reason, "SAR should answer that user-1 has no workspace access in %q", org2.Path().Join("workspace1"))
			require.Falsef(t, resp.Status.Allowed, "SAR should correctly answer that user-1 CANNOT list configmaps in %q because it has no access to it", org2.Path().Join("workspace1"))

			t.Logf("ask with normal SAR that user-1 can access %q because it has access", org1.Path().Join("workspace1"))
			resp, err = kubeClusterClient.Cluster(org1.Path().Join("workspace1")).AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
			require.NoError(t, err)
			require.Truef(t, resp.Status.Allowed, "SAR should correctly answer that user-1 CAN list configmaps in %q because it has access to %q", org2.Path().Join("workspace1"), org1.Path().Join("workspace1"))

			t.Logf("ask with deep SAR that user-1 hypothetically could list configmaps in %q if it had access", org2.Path().Join("workspace1"))
			deepSARClient, err := kcpkubernetesclientset.NewForConfig(authorization.WithDeepSARConfig(rest.CopyConfig(server.RootShardSystemMasterBaseConfig(t))))
			require.NoError(t, err)
			resp, err = deepSARClient.Cluster(org2.Path().Join("workspace1")).AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
			require.NoError(t, err)
			require.Truef(t, resp.Status.Allowed, "SAR should answer hypothetically that user-1 could list configmaps in %q if it had access", org2.Path().Join("workspace1"))
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

func createResources(ctx context.Context, t *testing.T, dynamicClusterClient kcpdynamic.ClusterInterface, discoveryClusterClient kcpdiscovery.DiscoveryClusterInterface, clusterName logicalcluster.Path, fileName string) {
	t.Helper()
	t.Logf("Create resources in %s", clusterName)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClusterClient.Cluster(clusterName)))
	require.Eventually(t, func() bool {
		if err := confighelpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(clusterName), mapper, nil, fileName, embeddedResources); err != nil {
			t.Logf("failed to create resources: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create resources")
}
