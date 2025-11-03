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

package proxy

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/authfixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestMappingWithClusterContext(t *testing.T) {
	framework.Suite(t, "control-plane")

	ctx := context.Background()

	// start kcp and setup clients;
	// note that the sharded-test-server will configure a special
	// `/e2e/clusters/{cluster}` route, which we are testing here
	server := kcptesting.SharedKcpServer(t)

	if len(server.ShardNames()) < 2 {
		t.Skip("This test requires a multi-shard setup with front-proxy.")
	}

	// The goal is to prove that having a {cluster} placeholder in the URL
	// correctly provides a cluster context to the underlying handler. This
	// handler is normally a custom virtual workspace, since kcp itself can
	// only handle health endpoints and /clusters/ URLs.
	// Instead of wiring up a custom virtual workspace, we just start a minimal
	// echo server to see if the correct headers are received.
	var lastHeaders http.Header
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastHeaders = r.Header.Clone()
	})

	srv := &http.Server{
		Handler: handler,
		BaseContext: func(l net.Listener) context.Context {
			return t.Context()
		},
		Addr: "localhost:2443", // as per front-proxy mapping
	}

	dirPath := filepath.Dir(server.KubeconfigPath())
	go func() {
		if err := srv.ListenAndServeTLS(
			filepath.Join(dirPath, "apiserver.crt"),
			filepath.Join(dirPath, "apiserver.key"),
		); err != nil {
			t.Logf("Error: %v", err)
		}
	}()

	baseWsPath, _ := kcptesting.NewWorkspaceFixture(t, server, logicalcluster.NewPath("root"), kcptesting.WithNamePrefix("oidc"))

	kcpConfig := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)
	kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
	require.NoError(t, err)

	// start a mock OIDC servers that will listen on a random port
	// (only for discovery and keyset handling, no actual login workflows)
	mock, ca := authfixtures.StartMockOIDC(t, server)

	// setup a new workspace auth config that uses mockoidc's server
	authConfig := authfixtures.CreateWorkspaceOIDCAuthentication(t, ctx, kcpClusterClient, baseWsPath, mock, ca, nil)

	// use these configs in new WorkspaceTypes
	wsType := authfixtures.CreateWorkspaceType(t, ctx, kcpClusterClient, baseWsPath, "with-oidc", authConfig)

	// create a new workspace with our new type
	t.Log("Creating Workspace...")
	teamPath, teamWs := kcptesting.NewWorkspaceFixture(t, server, baseWsPath, kcptesting.WithName("team-a"), kcptesting.WithType(baseWsPath, tenancyv1alpha1.WorkspaceTypeName(wsType)))
	teamCluster := teamWs.Spec.Cluster

	// grant permissions to the user
	authfixtures.GrantWorkspaceAccess(t, ctx, kubeClusterClient, teamPath, "grant-oidc-user", "cluster-admin", []rbacv1.Subject{{
		Kind: "User",
		Name: "oidc:user@example.com",
	}, {
		Kind: "Group",
		Name: "oidc:developers",
	}})

	var (
		username = "billybob"
		email    = "bob@example.com"
		groups   = []string{"developers"}

		expectedScope       = "cluster:" + teamCluster
		expectedClusterName = teamCluster
		expectedUsername    = "oidc:" + email
		expectedGroups      = []string{"system:authenticated"}
	)

	for _, group := range groups {
		expectedGroups = append(expectedGroups, "oidc:"+group)
	}

	token := authfixtures.CreateOIDCToken(t, mock, username, email, groups)

	cfg := framework.ConfigWithToken(token, kcpConfig)
	cfg.Host += "/e2e"

	client, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, _ = client.Cluster(teamPath).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})

		if lastHeaders == nil {
			return false
		}

		// if we find anything else in the auth infos, no need to try any further, neither the token
		// nor the OIDC mapping will change to produce other values
		require.Equal(t, expectedScope, lastHeaders.Get("X-Remote-Extra-Authentication.kcp.io%2fscopes"))
		require.Equal(t, expectedClusterName, lastHeaders.Get("X-Remote-Extra-Authentication.kcp.io%2fcluster-Name"))
		require.Equal(t, expectedUsername, lastHeaders.Get("X-Remote-User"))
		require.ElementsMatch(t, expectedGroups, lastHeaders.Values("X-Remote-Group"))

		return true
	}, wait.ForeverTestTimeout, 500*time.Millisecond)
}
