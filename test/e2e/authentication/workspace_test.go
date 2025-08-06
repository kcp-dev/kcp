/*
Copyright 2025 The KCP Authors.

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

package authentication

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xrstf/mockoidc"

	authenticationv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	"github.com/kcp-dev/kcp/sdk/testing/third_party/library-go/crypto"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestWorkspaceOIDC(t *testing.T) {
	framework.Suite(t, "control-plane")

	ctx := context.Background()

	// start kcp and setup clients
	server := kcptesting.SharedKcpServer(t)

	baseWsPath, _ := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"), kcptesting.WithNamePrefix("oidc"))

	kcpConfig := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)

	// start a two mock OIDC servers that will listen on random ports
	// (only for discovery and keyset handling, no actual login workflows)
	mockA, ca := startMockOIDC(t, server)
	mockB, _ := startMockOIDC(t, server)

	// setup a new workspace auth config that uses mockoidc's server, one for
	// each of our mockoidc servers
	authConfigA := createWorkspaceAuthentication(t, ctx, kcpClusterClient, baseWsPath, mockA, ca)
	authConfigB := createWorkspaceAuthentication(t, ctx, kcpClusterClient, baseWsPath, mockB, ca)

	// use these configs in new WorkspaceTypes and create one extra workspace type that allows
	// both mockoidc issuers
	wsTypeA := createWorkspaceType(t, ctx, kcpClusterClient, baseWsPath, authConfigA)
	wsTypeB := createWorkspaceType(t, ctx, kcpClusterClient, baseWsPath, authConfigB)
	wsTypeC := createWorkspaceType(t, ctx, kcpClusterClient, baseWsPath, authConfigA, authConfigB)

	// create a new workspace with our new type
	t.Log("Creating Workspaces...")
	teamAPath, _ := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("team-a"), kcptesting.WithType(baseWsPath, tenancyv1alpha1.WorkspaceTypeName(wsTypeA)))
	teamBPath, _ := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("team-b"), kcptesting.WithType(baseWsPath, tenancyv1alpha1.WorkspaceTypeName(wsTypeB)))
	teamCPath, _ := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("team-c"), kcptesting.WithType(baseWsPath, tenancyv1alpha1.WorkspaceTypeName(wsTypeC)))

	// sanity check: owner can access their own workspaces
	for _, path := range []logicalcluster.Path{teamAPath, teamBPath, teamCPath} {
		t.Logf("The workspace owner should be allowed to list ConfigMaps in %s...", path)
		_, err = kubeClusterClient.Cluster(path).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
	}

	// grant permissions to random users and groups
	grantWorkspaceAccess(t, ctx, kubeClusterClient, teamAPath, []rbacv1.Subject{{
		Kind: "User",
		Name: "oidc:user-a@example.com",
	}, {
		Kind: "Group",
		Name: "oidc:developers",
	}})

	grantWorkspaceAccess(t, ctx, kubeClusterClient, teamBPath, []rbacv1.Subject{{
		Kind: "User",
		Name: "oidc:user-b@example.com",
	}, {
		Kind: "Group",
		Name: "oidc:testers",
	}, {
		Kind: "Group",
		Name: "oidc:developers",
	}})

	grantWorkspaceAccess(t, ctx, kubeClusterClient, teamCPath, []rbacv1.Subject{{
		Kind: "User",
		Name: "oidc:user-c@example.com",
	}, {
		Kind: "Group",
		Name: "oidc:developers",
	}})

	// random user with random token should have no access
	randoKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(framework.ConfigWithToken("invalid-token", kcpConfig))
	require.NoError(t, err)

	for _, path := range []logicalcluster.Path{teamAPath, teamBPath, teamCPath} {
		t.Logf("An unauthenticated user shouldn't be able to list ConfigMaps in %s...", path)

		_, err = randoKubeClusterClient.Cluster(path).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
		require.Error(t, err)
	}

	testcases := []struct {
		name            string
		username        string
		email           string
		groups          []string
		mock            *mockoidc.MockOIDC
		workspaceAccess map[logicalcluster.Path]bool
	}{
		{
			name:     "user-a@example.com should be able to access workspace A only",
			username: "user-a",
			email:    "user-a@example.com",
			groups:   nil,
			mock:     mockA,
			workspaceAccess: map[logicalcluster.Path]bool{
				teamAPath: true,
				teamBPath: false,
				teamCPath: false, // user is authenticated but has no permissions in this workspace
			},
		},
		{
			name:     "user-b@example.com should be able to access workspace B only",
			username: "user-b",
			email:    "user-b@example.com",
			groups:   nil,
			mock:     mockB,
			workspaceAccess: map[logicalcluster.Path]bool{
				teamAPath: false,
				teamBPath: true,
				teamCPath: false, // user is authenticated but has no permissions in this workspace
			},
		},
		{
			name:     "user-a@example.com (developers) should be able to access workspace A and C",
			username: "user-a",
			email:    "user-a@example.com",
			groups:   []string{"developers"},
			mock:     mockA,
			workspaceAccess: map[logicalcluster.Path]bool{
				teamAPath: true,
				teamBPath: false,
				teamCPath: true,
			},
		},
		{
			name:     "Any user in the developers group should be able to access workspace A and C",
			username: "random-user",
			email:    "rando@example.com",
			groups:   []string{"developers"},
			mock:     mockA,
			workspaceAccess: map[logicalcluster.Path]bool{
				teamAPath: true,
				teamBPath: false, // false is correct since B does allow developers in, but only from its own issuer!
				teamCPath: true,
			},
		},
		{
			name:     "User C, signed by issuer A, is allowed to access workspace C",
			username: "user-c",
			email:    "user-c@example.com",
			groups:   nil,
			mock:     mockA,
			workspaceAccess: map[logicalcluster.Path]bool{
				teamAPath: false,
				teamBPath: false,
				teamCPath: true,
			},
		},
		{
			name:     "User C, signed by issuer B, is allowed to access workspace C",
			username: "user-c",
			email:    "user-c@example.com",
			groups:   nil,
			mock:     mockB,
			workspaceAccess: map[logicalcluster.Path]bool{
				teamAPath: false,
				teamBPath: false,
				teamCPath: true,
			},
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			t.Parallel()

			token := createOIDCToken(t, testcase.mock, testcase.username, testcase.email, testcase.groups)

			client, err := kcpkubernetesclientset.NewForConfig(framework.ConfigWithToken(token, kcpConfig))
			require.NoError(t, err)

			for workspace, expectedResult := range testcase.workspaceAccess {
				t.Run(workspace.Base(), func(t *testing.T) {
					t.Parallel()

					if expectedResult {
						// Initialization of the JWT authenticator happens asynchronously and depends both
						// on the cluster index knowing the WorkspaceType and the authenticator itself
						// performing OIDC discovery.

						require.Eventually(t, func() bool {
							_, err := client.Cluster(workspace).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})

							return err == nil
						}, wait.ForeverTestTimeout, 500*time.Millisecond)
					} else {
						_, err := client.Cluster(workspace).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
						require.Error(t, err, "user should have no access")
					}
				})
			}
		})
	}
}

func TestUserScope(t *testing.T) {
	framework.Suite(t, "control-plane")

	ctx := context.Background()

	// start kcp and setup clients
	server := kcptesting.SharedKcpServer(t)

	baseWsPath, _ := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"), kcptesting.WithNamePrefix("oidc-scope"))

	kcpConfig := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)

	mock, ca := startMockOIDC(t, server)
	authConfig := createWorkspaceAuthentication(t, ctx, kcpClusterClient, baseWsPath, mock, ca)
	wsType := createWorkspaceType(t, ctx, kcpClusterClient, baseWsPath, authConfig)

	// create a new workspace with our new type
	t.Log("Creating Workspaces...")
	teamPath, teamWs := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("team-a"), kcptesting.WithType(baseWsPath, tenancyv1alpha1.WorkspaceTypeName(wsType)))

	var (
		userName       = "peter"
		userEmail      = "peter@example.com"
		userGroups     = []string{"developers", "admins"}
		expectedGroups = []string{}
	)

	for _, group := range userGroups {
		expectedGroups = append(expectedGroups, "oidc:"+group)
	}

	grantWorkspaceAccess(t, ctx, kubeClusterClient, teamPath, []rbacv1.Subject{{
		Kind: "User",
		Name: "oidc:" + userEmail,
	}})

	token := createOIDCToken(t, mock, userName, userEmail, userGroups)

	peterClient, err := kcpkubernetesclientset.NewForConfig(framework.ConfigWithToken(token, kcpConfig))
	require.NoError(t, err)

	t.Logf("Creating SelfSubjectAccessReview in %s", teamPath)

	var review *authenticationv1.SelfSubjectReview
	require.Eventually(t, func() bool {
		request := &authenticationv1.SelfSubjectReview{}

		var err error
		review, err = peterClient.Cluster(teamPath).AuthenticationV1().SelfSubjectReviews().Create(ctx, request, metav1.CreateOptions{})
		if err != nil {
			t.Log(err)
		}

		return err == nil
	}, wait.ForeverTestTimeout, 500*time.Millisecond)

	user := review.Status.UserInfo
	require.Equal(t, "oidc:"+userEmail, user.Username)
	require.Subset(t, user.Groups, expectedGroups)
	require.Equal(t, user.Extra["authentication.kcp.io/scopes"], authenticationv1.ExtraValue{"cluster:" + teamWs.Spec.Cluster})
}

func createWorkspaceAuthentication(t *testing.T, ctx context.Context, client kcpclientset.ClusterInterface, workspace logicalcluster.Path, mock *mockoidc.MockOIDC, ca *crypto.CA) string {
	name := fmt.Sprintf("mockoidc-%d", rand.Int())

	// setup a new workspace auth config that uses mockoidc's server
	authConfig := &tenancyv1alpha1.WorkspaceAuthenticationConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: tenancyv1alpha1.WorkspaceAuthenticationConfigurationSpec{
			JWT: []tenancyv1alpha1.JWTAuthenticator{
				mockJWTAuthenticator(t, mock, ca),
			},
		},
	}

	t.Logf("Creating WorkspaceAuthenticationConfguration %s...", name)
	_, err := client.Cluster(workspace).TenancyV1alpha1().WorkspaceAuthenticationConfigurations().Create(ctx, authConfig, metav1.CreateOptions{})
	require.NoError(t, err)

	return name
}

func createWorkspaceType(t *testing.T, ctx context.Context, client kcpclientset.ClusterInterface, workspace logicalcluster.Path, authConfigNames ...string) string {
	name := fmt.Sprintf("with-oidc-%d", rand.Int())

	configs := []tenancyv1alpha1.AuthenticationConfigurationReference{}
	for _, name := range authConfigNames {
		configs = append(configs, tenancyv1alpha1.AuthenticationConfigurationReference{
			Name: name,
		})
	}

	// setup a new workspace auth config that uses mockoidc's server
	wsType := &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			AuthenticationConfigurations: configs,
		},
	}

	t.Logf("Creating WorkspaceType %s...", name)
	_, err := client.Cluster(workspace).TenancyV1alpha1().WorkspaceTypes().Create(ctx, wsType, metav1.CreateOptions{})
	require.NoError(t, err)

	return name
}

func grantWorkspaceAccess(t *testing.T, ctx context.Context, client kcpkubernetesclientset.ClusterInterface, workspace logicalcluster.Path, subjects []rbacv1.Subject) {
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "allow-oidc-user",
		},
		RoleRef: rbacv1.RoleRef{
			Kind: "ClusterRole",
			Name: "cluster-admin",
		},
		Subjects: subjects,
	}

	t.Log("Creating ClusterRoleBinding...")
	_, err := client.Cluster(workspace).RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{})
	require.NoError(t, err)
}
