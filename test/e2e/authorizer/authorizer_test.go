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

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
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

	usersKCPArgs, err := framework.Users([]framework.User{
		{
			Name:   "user-1",
			UID:    "1111-1111-1111-1111",
			Token:  "user-1-token",
			Groups: []string{"team-1"},
		},
		{
			Name:   "user-2",
			UID:    "1111-1111-1111-1111",
			Token:  "user-2-token",
			Groups: []string{"team-2"},
		},
		{
			Name:   "user-3",
			UID:    "1111-1111-1111-1111",
			Token:  "user-3-token",
			Groups: []string{"team-3"},
		},
	}).ArgsForKCP(t)
	require.NoError(t, err)

	f := framework.NewKcpFixture(t, framework.KcpConfig{
		Name: "main",
		Args: usersKCPArgs,
	})

	ctx := context.Background()
	if deadline, ok := t.Deadline(); ok {
		withDeadline, cancel := context.WithDeadline(ctx, deadline)
		t.Cleanup(cancel)
		ctx = withDeadline
	}
	require.Equal(t, len(f.Servers), 1, "incorrect number of servers")

	server := f.Servers["main"]

	kcpCfg, err := server.Config()
	require.NoError(t, err)
	kubeClusterClient, err := kubernetes.NewClusterForConfig(kcpCfg)
	require.NoError(t, err)
	kcpClusterClient, err := kcp.NewClusterForConfig(kcpCfg)
	require.NoError(t, err)
	dynamicClusterClient, err := dynamic.NewClusterForConfig(kcpCfg)
	require.NoError(t, err)

	orgClusterName := "admin"
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
		"user-1": newUserClient(t, "user-1", helper.EncodeOrganizationAndWorkspace(org, "workspace1"), kcpCfg),
		"user-2": newUserClient(t, "user-2", helper.EncodeOrganizationAndWorkspace(org, "workspace1"), kcpCfg),
		"user-3": newUserClient(t, "user-3", helper.EncodeOrganizationAndWorkspace(org, "workspace1"), kcpCfg),
	}

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(orgKcpClient.Discovery()))
	require.Eventually(t, func() bool {
		if err := confighelpers.CreateResourcesFromFS(ctx, clients["org"].Dynamic, mapper, embeddedResources); err != nil {
			t.Logf("failed to create resources: %v", err)
			return false
		}
		return true
	}, wait.ForeverTestTimeout, time.Millisecond*100, "failed to create resources")

	_, err = clients["user-1"].KubeClient.CoreV1().Namespaces().Create(
		ctx,
		&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)
	_, err = clients["user-1"].KubeClient.CoreV1().ConfigMaps("default").Create(
		ctx,
		&v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "foo"}},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	_, err = clients["user-2"].KubeClient.CoreV1().Namespaces().Create(
		ctx,
		&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "baz"}},
		metav1.CreateOptions{},
	)
	require.Error(t, err)
	_, err = clients["user-2"].KubeClient.CoreV1().ConfigMaps("default").Create(
		ctx,
		&v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "foo"}},
		metav1.CreateOptions{},
	)
	require.Error(t, err)

	_, err = clients["user-2"].KubeClient.CoreV1().Namespaces().Get(
		ctx, "default",
		metav1.GetOptions{},
	)
	require.NoError(t, err)
	_, err = clients["user-2"].KubeClient.CoreV1().ConfigMaps("default").Get(
		ctx, "foo",
		metav1.GetOptions{},
	)
	require.NoError(t, err)

	_, err = clients["user-3"].KubeClient.CoreV1().Namespaces().Create(
		ctx,
		&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "baz"}},
		metav1.CreateOptions{},
	)
	require.Error(t, err)
	_, err = clients["user-3"].KubeClient.CoreV1().ConfigMaps("default").Create(
		ctx,
		&v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "foo"}},
		metav1.CreateOptions{},
	)
	require.Error(t, err)
	_, err = clients["user-3"].KubeClient.CoreV1().Namespaces().Get(
		ctx, "default",
		metav1.GetOptions{},
	)
	require.Error(t, err)
	_, err = clients["user-3"].KubeClient.CoreV1().ConfigMaps("default").Get(
		ctx, "foo",
		metav1.GetOptions{},
	)
	require.Error(t, err)
}
