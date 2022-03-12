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

	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	kcp "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var embeddedResources embed.FS

type clients struct {
	KubeClient kubernetes.Interface
	KcpClient  kcp.Interface
	Dynamic    dynamic.Interface
}

func newUserClient(t *testing.T, username, clusterName string, cfg *rest.Config) *clients {
	cfgCopy := rest.CopyConfig(cfg)
	cfgCopy.Host = cfg.Host + "/clusters/" + clusterName
	cfgCopy.BearerToken = username + "-token"
	kubeClient, err := kubernetes.NewForConfig(cfgCopy)
	require.NoError(t, err)
	kcpClient, err := kcp.NewForConfig(cfgCopy)
	require.NoError(t, err)
	dynamicClient, err := dynamic.NewForConfig(cfgCopy)
	require.NoError(t, err)
	return &clients{
		KubeClient: kubeClient,
		KcpClient:  kcpClient,
		Dynamic:    dynamicClient,
	}
}

func TestAuthorizer(t *testing.T) {
	t.Parallel()

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	server := framework.SharedKcpServer(t)

	kcpCfg, err := server.DefaultConfig()
	require.NoError(t, err)
	kubeClusterClient, err := kubernetes.NewClusterForConfig(kcpCfg)
	require.NoError(t, err)
	kcpClusterClient, err := kcp.NewClusterForConfig(kcpCfg)
	require.NoError(t, err)
	dynamicClusterClient, err := dynamic.NewClusterForConfig(kcpCfg)
	require.NoError(t, err)

	orgClusterName := framework.NewOrganizationFixture(t, server)
	_, org, err := helper.ParseLogicalClusterName(orgClusterName)
	require.NoError(t, err)

	orgKubeClient := kubeClusterClient.Cluster(orgClusterName)
	orgKcpClient := kcpClusterClient.Cluster(orgClusterName)
	orgDynamicClient := dynamicClusterClient.Cluster(orgClusterName)

	clients := map[string]*clients{
		"org": {
			KubeClient: orgKubeClient,
			KcpClient:  orgKcpClient,
			Dynamic:    orgDynamicClient,
		},
		"user-1": newUserClient(t, "user-1", helper.EncodeOrganizationAndClusterWorkspace(org, "workspace1"), kcpCfg),
		"user-2": newUserClient(t, "user-2", helper.EncodeOrganizationAndClusterWorkspace(org, "workspace1"), kcpCfg),
		"user-3": newUserClient(t, "user-3", helper.EncodeOrganizationAndClusterWorkspace(org, "workspace1"), kcpCfg),
	}

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(orgKcpClient.Discovery()))
	require.Eventually(t, func() bool {
		if err := confighelpers.CreateResourcesFromFS(ctx, clients["org"].Dynamic, mapper, embeddedResources); err != nil {
			t.Logf("failed to create resources: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create resources")

	for _, wsName := range []string{"workspace1", "workspace2"} {
		require.Eventually(t, func() bool {
			ws, err := orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, "workspace1", metav1.GetOptions{})
			if err != nil {
				klog.Errorf("failed to get workspace: %v", err)
				return false
			}
			return ws.Status.Phase == tenancyv1alpha1.ClusterWorkspacePhaseReady
		}, wait.ForeverTestTimeout, time.Millisecond*100, "workspace %s didn't get ready", wsName)
	}

	tests := map[string]func(){
		"Users can view their own resources": func() {
			var err error
			_, err = clients["user-1"].KubeClient.CoreV1().Namespaces().Create(
				ctx,
				&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-own-resources"}},
				metav1.CreateOptions{},
			)
			require.NoError(t, err)
			_, err = clients["user-1"].KubeClient.CoreV1().ConfigMaps("test-own-resources").Create(
				ctx,
				&v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "test-own-resources"}},
				metav1.CreateOptions{},
			)
			require.NoError(t, err)

			_, err = clients["user-1"].KubeClient.CoreV1().Namespaces().Get(
				ctx, "test-own-resources",
				metav1.GetOptions{},
			)
			require.NoError(t, err)
			_, err = clients["user-1"].KubeClient.CoreV1().ConfigMaps("test-own-resources").Get(
				ctx, "test-own-resources",
				metav1.GetOptions{},
			)
			require.NoError(t, err)
		},
		"Users can view each others resources": func() {
			var err error
			_, err = clients["user-1"].KubeClient.CoreV1().Namespaces().Create(
				ctx,
				&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-each-other-resources"}},
				metav1.CreateOptions{},
			)
			require.NoError(t, err)
			_, err = clients["user-1"].KubeClient.CoreV1().ConfigMaps("test-each-other-resources").Create(
				ctx,
				&v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "test-each-other-resources"}},
				metav1.CreateOptions{},
			)
			require.NoError(t, err)

			_, err = clients["user-2"].KubeClient.CoreV1().Namespaces().Get(
				ctx, "test-each-other-resources",
				metav1.GetOptions{},
			)
			require.NoError(t, err)
			_, err = clients["user-2"].KubeClient.CoreV1().ConfigMaps("test-each-other-resources").Get(
				ctx, "test-each-other-resources",
				metav1.GetOptions{},
			)
			require.NoError(t, err)
		},
		"Users without access can not see resources": func() {
			var err error
			_, err = clients["user-1"].KubeClient.CoreV1().Namespaces().Create(
				ctx,
				&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "no-access-resources"}},
				metav1.CreateOptions{},
			)
			require.NoError(t, err)
			_, err = clients["user-1"].KubeClient.CoreV1().ConfigMaps("no-access-resources").Create(
				ctx,
				&v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "no-access-resources"}},
				metav1.CreateOptions{},
			)
			require.NoError(t, err)

			_, err = clients["user-3"].KubeClient.CoreV1().Namespaces().Get(
				ctx, "no-access-resources",
				metav1.GetOptions{},
			)
			require.Error(t, err)
			_, err = clients["user-3"].KubeClient.CoreV1().ConfigMaps("no-access-resources").Get(
				ctx, "no-access-resources",
				metav1.GetOptions{},
			)
			require.Error(t, err)
		},
		"Cluster Admins can use wildcard clusters": func() {
			var err error
			_, err = kubeClusterClient.Cluster("*").CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
			require.NoError(t, err)
		},
		"Non-Cluster Admins can not use wildcard clusters": func() {
			user1Config := rest.CopyConfig(kcpCfg)
			// Token is defined in test/e2e/framework/auth-tokens.csv
			user1Config.BearerToken = "user-1-token"
			user1ClusterConfig, err := kubernetes.NewClusterForConfig(user1Config)
			require.NoError(t, err)

			_, err = user1ClusterConfig.Cluster("*").CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
			require.Error(t, err, "Only cluster admins can use all clusters at once")
		},
	}

	for tcName, tcFunc := range tests {
		tcName := tcName
		tcFunc := tcFunc
		t.Run(tcName, func(t *testing.T) {
			t.Parallel()
			tcFunc()
		})
	}
}
