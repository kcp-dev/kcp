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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/xrstf/mockoidc"

	authenticationv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/authfixtures"
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

	// start two mock OIDC servers that will listen on random ports
	// (only for discovery and keyset handling, no actual login workflows)
	mockA, ca := authfixtures.StartMockOIDC(t, server)
	mockB, _ := authfixtures.StartMockOIDC(t, server)

	// setup a new workspace auth config that uses mockoidc's server, one for
	// each of our mockoidc servers
	authConfigA := authfixtures.CreateWorkspaceOIDCAuthentication(t, ctx, kcpClusterClient, baseWsPath, mockA, ca, nil)
	authConfigB := authfixtures.CreateWorkspaceOIDCAuthentication(t, ctx, kcpClusterClient, baseWsPath, mockB, ca, nil)

	// use these configs in new WorkspaceTypes and create one extra workspace type that allows
	// both mockoidc issuers
	wsTypeA := authfixtures.CreateWorkspaceType(t, ctx, kcpClusterClient, baseWsPath, "with-oidc-a", authConfigA)
	wsTypeB := authfixtures.CreateWorkspaceType(t, ctx, kcpClusterClient, baseWsPath, "with-oidc-b", authConfigB)
	wsTypeC := authfixtures.CreateWorkspaceType(t, ctx, kcpClusterClient, baseWsPath, "with-oidc-c", authConfigA, authConfigB)

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
	authfixtures.GrantWorkspaceAccess(t, ctx, kubeClusterClient, teamAPath, "grant-oidc-user", "cluster-admin", []rbacv1.Subject{{
		Kind: "User",
		Name: "oidc:user-a@example.com",
	}, {
		Kind: "Group",
		Name: "oidc:developers",
	}})

	authfixtures.GrantWorkspaceAccess(t, ctx, kubeClusterClient, teamBPath, "grant-oidc-user", "cluster-admin", []rbacv1.Subject{{
		Kind: "User",
		Name: "oidc:user-b@example.com",
	}, {
		Kind: "Group",
		Name: "oidc:testers",
	}, {
		Kind: "Group",
		Name: "oidc:developers",
	}})

	authfixtures.GrantWorkspaceAccess(t, ctx, kubeClusterClient, teamCPath, "grant-oidc-user", "cluster-admin", []rbacv1.Subject{{
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
		{
			name:     "impersonating system groups is not allowed and those groups will be stripped",
			username: "hacker",
			email:    "hacker@1337code.no",
			groups:   []string{bootstrap.SystemKcpAdminGroup, "system:masters"},
			mock:     mockB,
			workspaceAccess: map[logicalcluster.Path]bool{
				teamAPath: false,
				teamBPath: false,
				teamCPath: false,
			},
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			t.Parallel()

			token := authfixtures.CreateOIDCToken(t, testcase.mock, testcase.username, testcase.email, testcase.groups)

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

	mock, ca := authfixtures.StartMockOIDC(t, server)
	authConfig := authfixtures.CreateWorkspaceOIDCAuthentication(t, ctx, kcpClusterClient, baseWsPath, mock, ca,
		[]tenancyv1alpha1.ExtraMapping{
			{
				Key:             "authentication.kcp.io/scopes",
				ValueExpression: "['cluster:my-test-cluster']",
			},
			{
				Key:             "kcp.io/test",
				ValueExpression: "'test-value'",
			},
			{
				Key:             "other.example.com/test",
				ValueExpression: "'pass-value'",
			},
		},
	)
	wsType := authfixtures.CreateWorkspaceType(t, ctx, kcpClusterClient, baseWsPath, "with-oidc", authConfig)

	// create a new workspace with our new type
	t.Log("Creating Workspaces...")
	teamPath, teamWs := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("team-a"), kcptesting.WithType(baseWsPath, tenancyv1alpha1.WorkspaceTypeName(wsType)))

	var (
		userName       = "peter"
		userEmail      = "peter@example.com"
		userGroups     = []string{"developers", "admins"}
		expectedGroups = []string{"system:authenticated"}
		expectedExtras = map[string]authenticationv1.ExtraValue{
			// authentication.kcp.io/scopes from the extra mapping has
			// been scrubbed and only the expected cluster:<id> is set
			"authentication.kcp.io/scopes":       {"cluster:" + teamWs.Spec.Cluster},
			"authentication.kcp.io/cluster-name": {teamWs.Spec.Cluster},
			// kcp.io/test from the extra mapping should be scrubbed
			"other.example.com/test": {"pass-value"},
		}
	)

	for _, group := range userGroups {
		expectedGroups = append(expectedGroups, "oidc:"+group)
	}

	authfixtures.GrantWorkspaceAccess(t, ctx, kubeClusterClient, teamPath, "grant-oidc-user", "cluster-admin", []rbacv1.Subject{{
		Kind: "User",
		Name: "oidc:" + userEmail,
	}})

	token := authfixtures.CreateOIDCToken(t, mock, userName, userEmail, userGroups)

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
	require.Subset(t, user.Extra, expectedExtras)
}

func TestForbiddenSystemAccess(t *testing.T) {
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

	mock, ca := authfixtures.StartMockOIDC(t, server)

	// create an evil AuthConfig that would not prefix OIDC-provided groups, theoretically allowing
	// users to become part of system groups.
	// setup a new workspace auth config that uses mockoidc's server
	authConfig := &tenancyv1alpha1.WorkspaceAuthenticationConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: "evil-oidc",
		},
		Spec: tenancyv1alpha1.WorkspaceAuthenticationConfigurationSpec{
			JWT: []tenancyv1alpha1.JWTAuthenticator{
				authfixtures.MockJWTAuthenticator(t, mock, ca, "", ""),
			},
		},
	}

	t.Logf("Creating WorkspaceAuthenticationConfguration %s...", authConfig.Name)
	_, err = kcpClusterClient.Cluster(baseWsPath).TenancyV1alpha1().WorkspaceAuthenticationConfigurations().Create(ctx, authConfig, metav1.CreateOptions{})
	require.NoError(t, err)

	wsType := authfixtures.CreateWorkspaceType(t, ctx, kcpClusterClient, baseWsPath, "with-oidc", authConfig.Name)

	// create a new workspace with our new type
	t.Log("Creating Workspaces...")
	teamPath, _ := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("team-a"), kcptesting.WithType(baseWsPath, tenancyv1alpha1.WorkspaceTypeName(wsType)))

	// give a dummy user access
	authfixtures.GrantWorkspaceAccess(t, ctx, kubeClusterClient, teamPath, "grant-oidc-user", "cluster-admin", []rbacv1.Subject{{
		Kind: "User",
		Name: "dummy@example.com",
	}})

	// wait until the authenticator is ready
	token := authfixtures.CreateOIDCToken(t, mock, "dummy", "dummy@example.com", nil)

	client, err := kcpkubernetesclientset.NewForConfig(framework.ConfigWithToken(token, kcpConfig))
	require.NoError(t, err)

	t.Log("Waiting for authenticator to be ready...")
	require.Eventually(t, func() bool {
		_, err := client.Cluster(teamPath).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
		return err == nil
	}, wait.ForeverTestTimeout, 500*time.Millisecond)

	// Now that we know that the authenticator is ready, run the actual tests that ensure we do NOT
	// gain access based on our system names / groups.

	testcases := []struct {
		name     string
		username string
		email    string
		groups   []string
	}{
		{
			name:     fmt.Sprintf("%s should not give workspace access", bootstrap.SystemKcpAdminGroup),
			username: "al",
			email:    "al@bundy.com",
			groups:   []string{bootstrap.SystemKcpAdminGroup},
		},
		{
			name:     "shard-admin should not be admitted",
			username: "al",
			email:    "shard-admin",
			groups:   nil,
		},
	}

	t.Log("Testing tokens...")
	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			t.Parallel()

			token := authfixtures.CreateOIDCToken(t, mock, testcase.username, testcase.email, testcase.groups)

			client, err := kcpkubernetesclientset.NewForConfig(framework.ConfigWithToken(token, kcpConfig))
			require.NoError(t, err)

			_, err = client.Cluster(teamPath).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
			require.Error(t, err, "user should have no access")
		})
	}
}

func TestAcceptableWorkspaceAuthenticationConfigurations(t *testing.T) {
	framework.Suite(t, "control-plane")

	// start kcp and setup clients
	server := kcptesting.SharedKcpServer(t)

	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"), kcptesting.WithNamePrefix("oidc-acceptable"))

	kcpConfig := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)

	testcases := map[string]struct {
		authConfig    *tenancyv1alpha1.WorkspaceAuthenticationConfiguration
		expectedError string
	}{
		"empty": {
			authConfig:    &tenancyv1alpha1.WorkspaceAuthenticationConfiguration{},
			expectedError: "spec.jwt: Required value",
		},
		"minimal-claim": {
			authConfig: &tenancyv1alpha1.WorkspaceAuthenticationConfiguration{
				Spec: tenancyv1alpha1.WorkspaceAuthenticationConfigurationSpec{
					JWT: []tenancyv1alpha1.JWTAuthenticator{
						{
							Issuer: tenancyv1alpha1.Issuer{
								URL: "https://example.com",
							},
							ClaimMappings: tenancyv1alpha1.ClaimMappings{
								Username: tenancyv1alpha1.PrefixedClaimOrExpression{
									Claim: "email",
								},
								Groups: tenancyv1alpha1.PrefixedClaimOrExpression{
									Claim: "groups",
								},
							},
						},
					},
				},
			},
		},
		"minimal-expression": {
			authConfig: &tenancyv1alpha1.WorkspaceAuthenticationConfiguration{
				Spec: tenancyv1alpha1.WorkspaceAuthenticationConfigurationSpec{
					JWT: []tenancyv1alpha1.JWTAuthenticator{
						{
							Issuer: tenancyv1alpha1.Issuer{
								URL: "https://example.com",
							},
							ClaimMappings: tenancyv1alpha1.ClaimMappings{
								Username: tenancyv1alpha1.PrefixedClaimOrExpression{
									Expression: "concat('oidc:', email)",
								},
								Groups: tenancyv1alpha1.PrefixedClaimOrExpression{
									Expression: "groups",
								},
							},
						},
					},
				},
			},
		},
		"claim-and-expression": {
			authConfig: &tenancyv1alpha1.WorkspaceAuthenticationConfiguration{
				Spec: tenancyv1alpha1.WorkspaceAuthenticationConfigurationSpec{
					JWT: []tenancyv1alpha1.JWTAuthenticator{
						{
							Issuer: tenancyv1alpha1.Issuer{
								URL: "https://example.com",
							},
							ClaimMappings: tenancyv1alpha1.ClaimMappings{
								Username: tenancyv1alpha1.PrefixedClaimOrExpression{
									Claim:      "email",
									Expression: "concat('oidc:', email)",
								},
								Groups: tenancyv1alpha1.PrefixedClaimOrExpression{
									Claim:      "roles",
									Expression: "groups",
								},
							},
						},
					},
				},
			},
			expectedError: "claim and expression cannot both be specified",
		},
		"expression-and-prefix": {
			authConfig: &tenancyv1alpha1.WorkspaceAuthenticationConfiguration{
				Spec: tenancyv1alpha1.WorkspaceAuthenticationConfigurationSpec{
					JWT: []tenancyv1alpha1.JWTAuthenticator{
						{
							Issuer: tenancyv1alpha1.Issuer{
								URL: "https://example.com",
							},
							ClaimMappings: tenancyv1alpha1.ClaimMappings{
								Username: tenancyv1alpha1.PrefixedClaimOrExpression{
									Prefix:     ptr.To("random-prefix:"),
									Expression: "concat('oidc:', email)",
								},
								Groups: tenancyv1alpha1.PrefixedClaimOrExpression{
									Prefix:     ptr.To("group-random-prefix:"),
									Expression: "groups",
								},
							},
						},
					},
				},
			},
			expectedError: "prefix can only be specified when claim is specified",
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			t.Logf("Creating WorkspaceAuthenticationConfguration %s...", name)
			tc.authConfig.Name = name
			_, err := kcpClusterClient.Cluster(wsPath).TenancyV1alpha1().WorkspaceAuthenticationConfigurations().Create(t.Context(), tc.authConfig, metav1.CreateOptions{})
			if tc.expectedError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedError)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestWorkspaceOIDCTokenReview(t *testing.T) {
	framework.Suite(t, "control-plane")

	ctx := context.Background()

	// start kcp and setup clients
	server := kcptesting.SharedKcpServer(t)

	if len(server.ShardNames()) > 1 {
		t.Skip("This feature currently does not support multi shards because AuthConfigs are not replicated yet.")
	}

	baseWsPath, _ := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"), kcptesting.WithNamePrefix("workspace-auth-token-review"))

	kcpConfig := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)

	mock, ca := authfixtures.StartMockOIDC(t, server)
	authConfig := authfixtures.CreateWorkspaceOIDCAuthentication(t, ctx, kcpClusterClient, baseWsPath, mock, ca, nil)
	wsType := authfixtures.CreateWorkspaceType(t, ctx, kcpClusterClient, baseWsPath, "with-oidc", authConfig)

	// create a new workspace with our new type
	t.Log("Creating Workspaces...")
	teamPath, _ := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("team-a"), kcptesting.WithType(baseWsPath, tenancyv1alpha1.WorkspaceTypeName(wsType)))

	var (
		userName       = "peter"
		userEmail      = "peter@example.com"
		userGroups     = []string{"developers", "admins"}
		expectedGroups = []string{"system:authenticated"}
	)

	for _, group := range userGroups {
		expectedGroups = append(expectedGroups, "oidc:"+group)
	}

	authfixtures.GrantWorkspaceAccess(t, ctx, kubeClusterClient, teamPath, "grant-oidc-user", "cluster-admin", []rbacv1.Subject{{
		Kind: "User",
		Name: "oidc:" + userEmail,
	}})

	token := authfixtures.CreateOIDCToken(t, mock, userName, userEmail, userGroups)

	t.Logf("Creating TokenReview in %s", teamPath)

	const kcpDefaultAudience = "https://kcp.default.svc"

	review := &authenticationv1.TokenReview{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-review",
		},
		Spec: authenticationv1.TokenReviewSpec{
			Token:     token,
			Audiences: []string{kcpDefaultAudience},
		},
	}

	var response *authenticationv1.TokenReview
	require.Eventually(t, func() bool {
		var err error

		response, err = kubeClusterClient.Cluster(teamPath).AuthenticationV1().TokenReviews().Create(ctx, review, metav1.CreateOptions{})
		require.NoError(t, err)

		return response.Status.Authenticated
	}, wait.ForeverTestTimeout, 500*time.Millisecond)

	require.Contains(t, response.Status.Audiences, kcpDefaultAudience)
}
