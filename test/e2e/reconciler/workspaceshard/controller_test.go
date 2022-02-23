/*
Copyright 2021 The KCP Authors.

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

package workspaceshard

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	kubernetesclientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyv1alpha1client "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	utilconditions "github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

func TestWorkspaceShardController(t *testing.T) {
	t.Parallel()

	type runningServer struct {
		framework.RunningServer
		rootShardClient               tenancyv1alpha1client.WorkspaceShardInterface
		rootKubeClient, orgKubeClient kubernetesclientset.Interface
		expect                        framework.RegisterWorkspaceShardExpectation
	}
	var testCases = []struct {
		name string
		work func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "create a workspace shard without credentials, expect to see status reflect missing credentials",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("create a workspace shard without credentials")
				workspaceShard, err := server.rootShardClient.Create(ctx, &tenancyv1alpha1.WorkspaceShard{ObjectMeta: metav1.ObjectMeta{Name: "of-glass"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace shard")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.rootShardClient.Get(ctx, workspaceShard.Name, metav1.GetOptions{})
				})

				t.Logf("expecting workspace shard condition %s status=False reason=%q", tenancyv1alpha1.WorkspaceShardCredentialsValid, tenancyv1alpha1.WorkspaceShardCredentialsReasonMissing)
				require.Eventually(t, func() bool {
					workspaceShard, err = server.rootShardClient.Get(ctx, workspaceShard.Name, metav1.GetOptions{})
					require.NoError(t, err, "failed to get workspace shard")
					return utilconditions.IsFalse(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid) && utilconditions.GetReason(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid) == tenancyv1alpha1.WorkspaceShardCredentialsReasonMissing
				}, wait.ForeverTestTimeout, time.Second, "workspace shard condition %s updated to status=False reason=%q", tenancyv1alpha1.WorkspaceShardCredentialsValid, tenancyv1alpha1.WorkspaceShardCredentialsReasonMissing)
			},
		},
		{
			name: "create a workspace shard referencing missing credentials, expect to see status reflect missing credentials",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("create a workspace shard referencing missing credentials")
				workspaceShard, err := server.rootShardClient.Create(ctx, &tenancyv1alpha1.WorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{Name: "of-glass"},
					Spec: tenancyv1alpha1.WorkspaceShardSpec{Credentials: corev1.SecretReference{
						Name:      "not",
						Namespace: "real",
					}},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace shard")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.rootShardClient.Get(ctx, workspaceShard.Name, metav1.GetOptions{})
				})

				t.Logf("expecting workspace shard condition %s status=False reason=%q", tenancyv1alpha1.WorkspaceShardCredentialsValid, tenancyv1alpha1.WorkspaceShardCredentialsReasonMissing)
				require.Eventually(t, func() bool {
					workspaceShard, err = server.rootShardClient.Get(ctx, workspaceShard.Name, metav1.GetOptions{})
					require.NoError(t, err, "failed to get workspace shard")
					return utilconditions.IsFalse(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid) && utilconditions.GetReason(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid) == tenancyv1alpha1.WorkspaceShardCredentialsReasonMissing
				}, wait.ForeverTestTimeout, time.Second, "workspace shard condition %s updated to status=False reason=%q", tenancyv1alpha1.WorkspaceShardCredentialsValid, tenancyv1alpha1.WorkspaceShardCredentialsReasonMissing)
			},
		},
		{
			name: "create a workspace shard referencing credentials without data, expect to see status reflect invalid credentials",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("create a namespace %q for the credentials secret", "credentials")
				_, err := server.rootKubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "credentials"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create credentials namespace")

				t.Logf("create credential secret without credentials")
				secret, err := server.rootKubeClient.CoreV1().Secrets("credentials").Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "kubeconfig"},
					Data: map[string][]byte{
						"unrelated": []byte(`information`),
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create credentials secret")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.rootKubeClient.CoreV1().Secrets(secret.Namespace).Get(ctx, secret.Name, metav1.GetOptions{})
				})

				t.Logf("create a workspace shard referencing the secret above")
				workspaceShard, err := server.rootShardClient.Create(ctx, &tenancyv1alpha1.WorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{Name: "of-glass"},
					Spec: tenancyv1alpha1.WorkspaceShardSpec{Credentials: corev1.SecretReference{
						Name:      "kubeconfig",
						Namespace: "credentials",
					}},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace shard")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.rootShardClient.Get(ctx, workspaceShard.Name, metav1.GetOptions{})
				})

				t.Logf("expecting workspace shard condition %s status=False reason=%q", tenancyv1alpha1.WorkspaceShardCredentialsValid, tenancyv1alpha1.WorkspaceShardCredentialsReasonInvalid)
				require.Eventually(t, func() bool {
					workspaceShard, err = server.rootShardClient.Get(ctx, workspaceShard.Name, metav1.GetOptions{})
					require.NoError(t, err, "failed to get workspace shard")
					return utilconditions.IsFalse(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid) && utilconditions.GetReason(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid) == tenancyv1alpha1.WorkspaceShardCredentialsReasonInvalid
				}, wait.ForeverTestTimeout, time.Second, "workspace shard condition %s updated to status=False reason=%q", tenancyv1alpha1.WorkspaceShardCredentialsValid, tenancyv1alpha1.WorkspaceShardCredentialsReasonInvalid)
			},
		},
		{
			name: "create a workspace shard referencing credentials with invalid data, expect to see status reflect invalid credentials",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("create a namespace %q for the credentials secret", "credentials")
				_, err := server.rootKubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "credentials"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create credentials namespace")

				t.Logf("create credential secret with an invalid kubeconfig file")
				secret, err := server.rootKubeClient.CoreV1().Secrets("credentials").Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "kubeconfig"},
					Data: map[string][]byte{
						"kubeconfig": []byte(`not a kubeconfig`),
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create credentials secret")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.rootKubeClient.CoreV1().Secrets(secret.Namespace).Get(ctx, secret.Name, metav1.GetOptions{})
				})

				t.Logf("create a workspace shard referencing the secret above")
				workspaceShard, err := server.rootShardClient.Create(ctx, &tenancyv1alpha1.WorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{Name: "of-glass"},
					Spec: tenancyv1alpha1.WorkspaceShardSpec{Credentials: corev1.SecretReference{
						Name:      "kubeconfig",
						Namespace: "credentials",
					}},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace shard")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.rootShardClient.Get(ctx, workspaceShard.Name, metav1.GetOptions{})
				})

				t.Logf("expecting workspace shard condition %s status=False reason=%q", tenancyv1alpha1.WorkspaceShardCredentialsValid, tenancyv1alpha1.WorkspaceShardCredentialsReasonInvalid)
				require.Eventually(t, func() bool {
					workspaceShard, err = server.rootShardClient.Get(ctx, workspaceShard.Name, metav1.GetOptions{})
					require.NoError(t, err, "failed to get workspace shard")
					return utilconditions.IsFalse(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid) && utilconditions.GetReason(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid) == tenancyv1alpha1.WorkspaceShardCredentialsReasonInvalid
				}, wait.ForeverTestTimeout, time.Second, "workspace shard condition %s updated to status=False reason=%q", tenancyv1alpha1.WorkspaceShardCredentialsValid, tenancyv1alpha1.WorkspaceShardCredentialsReasonInvalid)
			},
		},
		{
			name: "create a workspace shard referencing valid credentials, expect to see status reflect that, and then notice a change in the secret",
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				t.Logf("create a namespace %q for the credentials secret", "credentials")
				_, err := server.rootKubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "credentials"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create credentials namespace")

				t.Logf("create credential secret with a valid kubeconfig file")
				rawCfg := clientcmdapi.Config{
					Clusters:       map[string]*clientcmdapi.Cluster{"cluster": {Server: "https://kcp.dev/apiprefix"}},
					Contexts:       map[string]*clientcmdapi.Context{"context": {Cluster: "cluster", AuthInfo: "user"}},
					CurrentContext: "context",
					AuthInfos:      map[string]*clientcmdapi.AuthInfo{"user": {Username: "user", Password: "password"}},
				}
				rawBytes, err := clientcmd.Write(rawCfg)
				require.NoError(t, err, "could not serialize raw config")

				cfg, err := clientcmd.NewNonInteractiveClientConfig(rawCfg, "context", nil, nil).ClientConfig()
				require.NoError(t, err, "failed to create rootShardClient config")

				secret, err := server.rootKubeClient.CoreV1().Secrets("credentials").Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "kubeconfig"},
					Data: map[string][]byte{
						"kubeconfig": rawBytes,
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create credentials secret")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.rootKubeClient.CoreV1().Secrets(secret.Namespace).Get(ctx, secret.Name, metav1.GetOptions{})
				})

				t.Logf("create a workspace shard referencing the secret above")
				workspaceShard, err := server.rootShardClient.Create(ctx, &tenancyv1alpha1.WorkspaceShard{
					ObjectMeta: metav1.ObjectMeta{Name: "of-glass"},
					Spec: tenancyv1alpha1.WorkspaceShardSpec{Credentials: corev1.SecretReference{
						Name:      "kubeconfig",
						Namespace: "credentials",
					}},
				}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace shard")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.rootShardClient.Get(ctx, workspaceShard.Name, metav1.GetOptions{})
				})

				t.Logf("expecting workspace shard condition %s status=True with correct connection info", tenancyv1alpha1.WorkspaceShardCredentialsValid)
				var originalHash string
				require.Eventually(t, func() bool {
					workspaceShard, err = server.rootShardClient.Get(ctx, workspaceShard.Name, metav1.GetOptions{})
					require.NoError(t, err, "failed to get workspace shard")
					if !utilconditions.IsTrue(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid) {
						return false
					}
					if diff := cmp.Diff(workspaceShard.Status.ConnectionInfo, &tenancyv1alpha1.ConnectionInfo{
						Host:    cfg.Host,
						APIPath: cfg.APIPath,
					}); diff != "" {
						t.Logf("workspace shard connection info differs from expected: %s", diff)
						return false
					}
					originalHash = workspaceShard.Status.CredentialsHash
					return true
				}, wait.ForeverTestTimeout, time.Second, "workspace shard condition %s updated to status=True", tenancyv1alpha1.WorkspaceShardCredentialsValid)

				t.Logf("update credential secret with a valid kubeconfig file")
				rawCfg.AuthInfos["user"].Password = "rotated"
				rawBytes, err = clientcmd.Write(rawCfg)
				require.NoError(t, err, "could not serialize raw config")

				secret.Data["kubeconfig"] = rawBytes
				_, err = server.rootKubeClient.CoreV1().Secrets("credentials").Update(ctx, secret, metav1.UpdateOptions{})
				require.NoError(t, err, "failed to create credentials secret")

				t.Logf("expecting workspace shard condition %s status=True with new credential hash", tenancyv1alpha1.WorkspaceShardCredentialsValid)
				require.Eventually(t, func() bool {
					workspaceShard, err = server.rootShardClient.Get(ctx, workspaceShard.Name, metav1.GetOptions{})
					require.NoError(t, err, "failed to get workspace shard")
					if !utilconditions.IsTrue(workspaceShard, tenancyv1alpha1.WorkspaceShardCredentialsValid) {
						return false
					}
					if workspaceShard.Status.CredentialsHash == originalHash {
						t.Logf("workspace shard credential hash did not change")
						return false
					}
					return true
				}, wait.ForeverTestTimeout, time.Second, "workspace shard condition %s updated to status=True with new credential hash", tenancyv1alpha1.WorkspaceShardCredentialsValid)
			},
		},
	}
	const serverName = "main"
	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// TODO(marun) Refactor tests to enable the use of shared fixture
			f := framework.NewKcpFixture(t,
				framework.KcpConfig{
					Name: serverName,
				},
			)

			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				withDeadline, cancel := context.WithDeadline(ctx, deadline)
				t.Cleanup(cancel)
				ctx = withDeadline
			}

			require.Equal(t, 1, len(f.Servers), "incorrect number of servers")
			server := f.Servers[serverName]

			cfg, err := server.Config("system:admin")
			require.NoError(t, err)

			orgClusterName := framework.NewOrganizationFixture(t, f.Servers[serverName])

			kcpClients, err := kcpclientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct kcp rootShardClient for server")

			rootKcpClient := kcpClients.Cluster(helper.RootCluster)
			expect, err := framework.ExpectWorkspaceShards(ctx, t, rootKcpClient)
			require.NoError(t, err, "failed to start expecter")

			kubeClients, err := kubernetesclientset.NewClusterForConfig(cfg)
			require.NoError(t, err, "failed to construct kube rootShardClient for server")

			testCase.work(ctx, t, runningServer{
				RunningServer:   server,
				rootShardClient: rootKcpClient.TenancyV1alpha1().WorkspaceShards(),
				rootKubeClient:  kubeClients.Cluster(helper.RootCluster),
				orgKubeClient:   kubeClients.Cluster(orgClusterName),
				expect:          expect,
			})
		})
	}
}
