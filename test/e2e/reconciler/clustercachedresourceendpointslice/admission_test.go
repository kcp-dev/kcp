/*
Copyright 2025 The kcp Authors.

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

package clustercachedresourceendpointslice

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestClusterCachedResourceEndpointSliceAdmission verifies that referencing a
// ClusterCachedResource in another workspace from a ClusterCachedResourceEndpointSlice
// requires the "bind" verb on that ClusterCachedResource in its own workspace.
func TestClusterCachedResourceEndpointSliceAdmission(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx := t.Context()
	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kcpApiExtensionClusterClient, err := kcpapiextensionsclientset.NewForConfig(cfg)
	require.NoError(t, err)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"))
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer"))

	// user-2 is admin of the consumer workspace and has no access to the provider workspace.
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, consumerPath, []string{"user-2"}, nil, true)

	sheriffsGR := metav1.GroupResource{Group: "wildwest.dev", Resource: "sheriffs"}
	t.Logf("Creating wildwest.dev CRD and ClusterCachedResource in provider workspace %q", providerPath)
	wildwest.Create(t, providerPath, kcpApiExtensionClusterClient.ApiextensionsV1().CustomResourceDefinitions(), sheriffsGR)

	clusterCachedResource := &cachev1alpha1.ClusterCachedResource{
		ObjectMeta: metav1.ObjectMeta{Name: sheriffsGR.String()},
		Spec: cachev1alpha1.ClusterCachedResourceSpec{
			GroupVersionResource: cachev1alpha1.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "sheriffs"},
		},
	}
	clusterCachedResourceClient := kcpClusterClient.CacheV1alpha1().ClusterCachedResources()
	_, err = clusterCachedResourceClient.Cluster(providerPath).Create(ctx, clusterCachedResource, metav1.CreateOptions{})
	require.NoError(t, err)
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return clusterCachedResourceClient.Cluster(providerPath).Get(ctx, clusterCachedResource.Name, metav1.GetOptions{})
	}, kcptestinghelpers.Is(cachev1alpha1.ReplicationStarted), "ClusterCachedResource should start replicating")

	user2KcpClient, err := kcpclientset.NewForConfig(framework.StaticTokenUserConfig("user-2", cfg))
	require.NoError(t, err)
	user2SliceClient := user2KcpClient.CacheV1alpha1().ClusterCachedResourceEndpointSlices().Cluster(consumerPath)

	newSlice := func() *cachev1alpha1.ClusterCachedResourceEndpointSlice {
		return &cachev1alpha1.ClusterCachedResourceEndpointSlice{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "foreign-"},
			Spec: cachev1alpha1.ClusterCachedResourceEndpointSliceSpec{
				ClusterCachedResource: cachev1alpha1.ClusterCachedResourceReference{
					Path: providerPath.String(),
					Name: clusterCachedResource.Name,
				},
			},
		}
	}

	t.Logf("user-2 must not be able to reference the ClusterCachedResource in %q without bind permission", providerPath)
	_, err = user2SliceClient.Create(ctx, newSlice(), metav1.CreateOptions{})
	require.True(t, apierrors.IsForbidden(err), "expected forbidden, got: %v", err)
	require.ErrorContains(t, err, fmt.Sprintf("no permission to bind to ClusterCachedResource %s:%s", providerPath, clusterCachedResource.Name))

	t.Logf("user-2 must not be able to reference a non-existent ClusterCachedResource either, with the same error")
	missing := newSlice()
	missing.Spec.ClusterCachedResource.Name = "does-not-exist"
	_, err = user2SliceClient.Create(ctx, missing, metav1.CreateOptions{})
	require.True(t, apierrors.IsForbidden(err), "expected forbidden, got: %v", err)
	require.ErrorContains(t, err, "no permission to bind to ClusterCachedResource")

	t.Logf("Granting user-2 bind on the ClusterCachedResource in %q", providerPath)
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "clustercachedresource-bind"},
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{cachev1alpha1.SchemeGroupVersion.Group},
			Resources:     []string{"clustercachedresources"},
			ResourceNames: []string{clusterCachedResource.Name},
			Verbs:         []string{"bind"},
		}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "clustercachedresource-bind-user-2"},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "user-2", APIGroup: rbacv1.GroupName}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "clustercachedresource-bind"},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("user-2 can now reference the ClusterCachedResource in %q", providerPath)
	var sliceName string
	// The delegated authorizer caches denials for 30s, hence the generous timeout.
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		created, err := user2SliceClient.Create(ctx, newSlice(), metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		sliceName = created.Name
		return true, ""
	}, 2*time.Minute, time.Second, "expected slice creation to succeed once bind is granted")

	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return user2SliceClient.Get(ctx, sliceName, metav1.GetOptions{})
	}, kcptestinghelpers.Is(cachev1alpha1.ClusterCachedResourceValid), "slice should become valid")
}
