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

package clusterworkspaces

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	virtualcommand "github.com/kcp-dev/kcp/cmd/virtual-workspaces/command"
	virtualoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestStandaloneWorkspacesVirtualWorkspaces(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")
	if len(framework.TestConfig.KCPKubeconfig()) != 0 {
		t.Skip("Skip testing standalone when running against persistent fixture to minimize test execution cost for development")
	}

	t.Skip("TODO reenable this test as part of the work to resolve https://github.com/kcp-dev/kcp/issues/2056")

	t.Run("Standalone virtual workspace apiserver", func(t *testing.T) {
		t.Parallel()
		testWorkspacesVirtualWorkspaces(t, true)
	})
}

func TestInProcessWorkspacesVirtualWorkspaces(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")
	t.Run("In-process virtual workspace apiserver", func(t *testing.T) {
		t.Parallel()
		testWorkspacesVirtualWorkspaces(t, false)
	})
}

type runningServer struct {
	framework.RunningServer
	orgClusterName       logicalcluster.Path
	kcpClusterClient     kcpclientset.ClusterInterface
	virtualClusterClient kcpclientset.ClusterInterface
}

var testCases = []struct {
	name string
	work func(ctx context.Context, t *testing.T, server runningServer)
}{
	{
		name: "create a clusterworkspace via projection and see the workspace",
		work: func(ctx context.Context, t *testing.T, server runningServer) {
			t.Logf("Create ClusterWorkspace workspace1 in the virtual workspace")
			workspace1, err := server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{Name: "workspace1"},
			}, metav1.CreateOptions{})
			require.NoError(t, err)

			t.Logf("Verify that the ClusterWorkspace results in a Workspace of the same name in the org workspace")
			_, err = server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
			require.NoError(t, err, "expected to see workspace1 as Workspace")

			t.Logf("Verify that the ClusterWorkspace results in a ClusterWorkspace of the same name in the org workspace")
			_, err = server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
			require.NoError(t, err, "expected to see workspace1 as Workspace")

			t.Logf("ClusterWorkspace will show up in a list")
			list, err := server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().List(ctx, metav1.ListOptions{})
			require.NoError(t, err)
			require.Len(t, list.Items, 1)

			t.Logf("Deleting ClusterWorkspace results in Workspace deletion")
			err = server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Delete(ctx, workspace1.Name, metav1.DeleteOptions{})
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				if _, err = server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().Get(ctx, workspace1.Name, metav1.GetOptions{}); kerrors.IsNotFound(err) {
					return true
				}
				require.NoError(t, err)
				return false
			}, wait.ForeverTestTimeout, time.Millisecond*100, "expected Workspace to be deleted")

			t.Logf("Deleting ClusterWorkspace results in ClusterWorkspace deletion")
			_, err = server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
			if !kerrors.IsNotFound(err) {
				require.NoError(t, err)
			}
		},
	},
	{
		name: "create a workspace and see the clusterworkspace",
		work: func(ctx context.Context, t *testing.T, server runningServer) {
			t.Logf("Create Workspace workspace1 in the virtual workspace")
			workspace1, err := server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1beta1().Workspaces().Create(ctx, &tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "workspace1"},
			}, metav1.CreateOptions{})
			require.NoError(t, err)

			t.Logf("Verify that the Workspace results in a ClusterWorkspace of the same name in the org workspace")
			_, err = server.kcpClusterClient.Cluster(server.orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
			require.NoError(t, err, "expected to see workspace1 as Workspace")
		},
	},
}

func testWorkspacesVirtualWorkspaces(t *testing.T, standalone bool) {
	var server framework.RunningServer
	var virtualWorkspaceServerHost string
	if standalone {
		// create port early. We have to hope it is still free when we are ready to start the virtual workspace apiserver.
		portStr, err := framework.GetFreePort(t)
		require.NoError(t, err)

		tokenAuthFile := framework.WriteTokenAuthFile(t)
		server = framework.PrivateKcpServer(t,
			framework.WithCustomArguments(append(framework.TestServerArgsWithTokenAuthFile(tokenAuthFile),
				"--run-virtual-workspaces=false",
				fmt.Sprintf("--shard-virtual-workspace-url=https://localhost:%s", portStr),
			)...,
			))

		// write kubeconfig to disk, next to kcp kubeconfig
		kcpAdminConfig, _ := server.RawConfig()
		var baseCluster = *kcpAdminConfig.Clusters["base"] // shallow copy
		baseCluster.Server = fmt.Sprintf("%s/clusters/system:admin", baseCluster.Server)
		virtualWorkspaceKubeConfig := clientcmdapi.Config{
			Clusters: map[string]*clientcmdapi.Cluster{
				"shard": &baseCluster,
			},
			Contexts: map[string]*clientcmdapi.Context{
				"shard": {
					Cluster:  "shard",
					AuthInfo: "virtualworkspace",
				},
			},
			AuthInfos: map[string]*clientcmdapi.AuthInfo{
				"virtualworkspace": kcpAdminConfig.AuthInfos["shard-admin"],
			},
			CurrentContext: "shard",
		}
		kubeconfigPath := filepath.Join(filepath.Dir(server.KubeconfigPath()), "virtualworkspace.kubeconfig")
		err = clientcmd.WriteToFile(virtualWorkspaceKubeConfig, kubeconfigPath)
		require.NoError(t, err)

		// launch virtual workspace apiserver
		port, err := strconv.Atoi(portStr)
		require.NoError(t, err)
		opts := virtualoptions.NewOptions()
		opts.KubeconfigFile = kubeconfigPath
		opts.SecureServing.BindPort = port
		opts.SecureServing.ServerCert.CertKey.KeyFile = filepath.Join(filepath.Dir(server.KubeconfigPath()), "apiserver.key")
		opts.SecureServing.ServerCert.CertKey.CertFile = filepath.Join(filepath.Dir(server.KubeconfigPath()), "apiserver.crt")
		opts.Authentication.SkipInClusterLookup = true
		opts.Authentication.RemoteKubeConfigFile = kubeconfigPath
		err = opts.Validate()
		require.NoError(t, err)
		ctx, cancelFunc := context.WithCancel(context.Background())
		t.Cleanup(cancelFunc)
		go func() {
			err = virtualcommand.Run(ctx, opts)
			require.NoError(t, err)
		}()

		// wait for readiness
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
		baseClusterServerURL, err := url.Parse(baseCluster.Server)
		require.NoError(t, err)
		virtualWorkspaceServerHost = fmt.Sprintf("https://%s:%s", baseClusterServerURL.Hostname(), portStr)

		require.Eventually(t, func() bool {
			resp, err := client.Get(fmt.Sprintf("%s/readyz", virtualWorkspaceServerHost))
			if err != nil {
				t.Logf("error checking virtual workspace readiness: %v", err)
				return false
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
			t.Logf("virtual workspace is not ready yet, status code: %d", resp.StatusCode)
			return false
		}, wait.ForeverTestTimeout, time.Millisecond*100, "virtual workspace apiserver not ready")
	} else {
		server = framework.SharedKcpServer(t)
		virtualWorkspaceServerHost = server.BaseConfig(t).Host
	}

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			orgClusterName := framework.NewOrganizationFixture(t, server, framework.WithShardConstraints(tenancyv1alpha1.ShardConstraints{Name: "root"}))

			// create non-virtual clients
			kcpConfig := server.RootShardSystemMasterBaseConfig(t)
			kcpClusterClient, err := kcpclientset.NewForConfig(kcpConfig)
			require.NoError(t, err, "failed to construct client for server")

			vwConfig := rest.CopyConfig(kcpConfig)
			vwConfig.Host = virtualWorkspaceServerHost + path.Join(virtualoptions.DefaultRootPathPrefix, "clusterworkspaces")
			vwKcpClusterClient, err := kcpclientset.NewForConfig(vwConfig)

			testCase.work(ctx, t, runningServer{
				RunningServer:        server,
				orgClusterName:       orgClusterName,
				kcpClusterClient:     kcpClusterClient,
				virtualClusterClient: vwKcpClusterClient,
			})
		})
	}
}
