/*
Copyright 2026 The kcp Authors.

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

package apibinding

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/utils/ptr"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestDefaultAPIBindingsBindPermission verifies that creating or updating a
// WorkspaceType with spec.defaultAPIBindings requires the user to hold the
// "bind" verb on each referenced APIExport.
//
// Without this check, an unprivileged user with permission to create
// WorkspaceTypes and Workspaces could cause the default-apibinding-controller
// (which runs with system credentials) to create APIBindings to APIExports the
// user has no "bind" permission on. See pkg/admission/workspacetype.
func TestDefaultAPIBindingsBindPermission(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client")

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	// Provider workspace owns the cowboys APIExport. user-1 has NO permissions here.
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	t.Logf("provider workspace: %s", providerPath)

	// Tenant workspace is where user-1 has cluster-admin and tries to create the
	// malicious WorkspaceType. This mirrors the PoC in the security report.
	tenantPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	t.Logf("tenant workspace: %s", tenantPath)

	t.Logf("Install cowboys APIResourceSchema into provider workspace %q", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create cowboys APIExport in provider workspace %q", providerPath)
	cowboysAPIExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "today-cowboys"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "wildwest.dev",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Grant user-1 cluster-admin in tenant workspace %q (matches PoC)", tenantPath)
	framework.AdmitWorkspaceAccess(t.Context(), t, kubeClusterClient, tenantPath, []string{"user-1"}, nil, true)

	user1Cfg := framework.StaticTokenUserConfig("user-1", rest.CopyConfig(cfg))
	user1KcpClient, err := kcpclientset.NewForConfig(user1Cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for user-1")

	providerPathStr := providerPath.String()

	t.Run("create denied without bind permission", func(t *testing.T) {
		// user-1 has no "bind" permission on cowboys. Creating a WorkspaceType
		// in the tenant workspace that references the cowboys APIExport in
		// defaultAPIBindings must be rejected by admission.
		wt := &tenancyv1alpha1.WorkspaceType{
			ObjectMeta: metav1.ObjectMeta{Name: "bad"},
			Spec: tenancyv1alpha1.WorkspaceTypeSpec{
				Extend: tenancyv1alpha1.WorkspaceTypeExtension{
					With: []tenancyv1alpha1.WorkspaceTypeReference{{Name: "universal", Path: "root"}},
				},
				DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
					{Path: providerPathStr, Export: cowboysAPIExport.Name},
				},
				DefaultAPIBindingLifecycle: ptr.To(tenancyv1alpha1.APIBindingLifecycleModeMaintain),
			},
		}

		// The bind-permission check is the only gate that lets this Create
		// through; user-1 has no bind on the APIExport, so it MUST fail every
		// time. We retry only because admission may transiently return a
		// generic "not yet ready to handle request" forbidden until its
		// informers sync — once ready, the message is the deterministic
		// "no permission to bind to export ...". A nil error here would mean
		// the security check did not fire, which is a regression, so we
		// hard-fail rather than retry past it.
		expected := fmt.Sprintf("no permission to bind to export %s:%s", providerPathStr, cowboysAPIExport.Name)
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := user1KcpClient.Cluster(tenantPath).TenancyV1alpha1().WorkspaceTypes().Create(t.Context(), wt, metav1.CreateOptions{})
			require.Error(t, err, "WorkspaceType create must be denied: user-1 has no bind permission on %s:%s", providerPathStr, cowboysAPIExport.Name)
			if !strings.Contains(err.Error(), expected) {
				return false, fmt.Sprintf("waiting for deterministic admission error: want %q, got %q", expected, err.Error())
			}
			return true, ""
		}, wait.ForeverTestTimeout, 250*time.Millisecond, "expected denial of WorkspaceType create with deterministic bind error")
	})

	t.Run("create allowed when user has bind permission", func(t *testing.T) {
		t.Logf("Grant user-1 bind on cowboys APIExport in provider workspace %q", providerPath)
		clusterRole, clusterRoleBinding := createClusterRoleAndBindings(
			"user-1-bind-cowboys", "user-1", "User",
			apisv1alpha2.SchemeGroupVersion.Group, "apiexports", cowboysAPIExport.Name,
			[]string{"bind"},
		)
		_, err := kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoles().Create(t.Context(), clusterRole, metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), clusterRoleBinding, metav1.CreateOptions{})
		require.NoError(t, err)

		wt := &tenancyv1alpha1.WorkspaceType{
			ObjectMeta: metav1.ObjectMeta{Name: "good"},
			Spec: tenancyv1alpha1.WorkspaceTypeSpec{
				Extend: tenancyv1alpha1.WorkspaceTypeExtension{
					With: []tenancyv1alpha1.WorkspaceTypeReference{{Name: "universal", Path: "root"}},
				},
				DefaultAPIBindings: []tenancyv1alpha1.APIExportReference{
					{Path: providerPathStr, Export: cowboysAPIExport.Name},
				},
				DefaultAPIBindingLifecycle: ptr.To(tenancyv1alpha1.APIBindingLifecycleModeMaintain),
			},
		}

		// Wait for the SAR cache + informers to observe the new RBAC.
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := user1KcpClient.Cluster(tenantPath).TenancyV1alpha1().WorkspaceTypes().Create(t.Context(), wt, metav1.CreateOptions{})
			if err == nil {
				return true, ""
			}
			return false, err.Error()
		}, wait.ForeverTestTimeout, 250*time.Millisecond, "expected WorkspaceType create to succeed once user-1 has bind")
	})

	t.Run("update adding new defaultAPIBinding requires bind", func(t *testing.T) {
		t.Logf("Create empty WorkspaceType as user-1")
		wt := &tenancyv1alpha1.WorkspaceType{
			ObjectMeta: metav1.ObjectMeta{Name: "later"},
			Spec: tenancyv1alpha1.WorkspaceTypeSpec{
				Extend: tenancyv1alpha1.WorkspaceTypeExtension{
					With: []tenancyv1alpha1.WorkspaceTypeReference{{Name: "universal", Path: "root"}},
				},
			},
		}
		created, err := user1KcpClient.Cluster(tenantPath).TenancyV1alpha1().WorkspaceTypes().Create(t.Context(), wt, metav1.CreateOptions{})
		require.NoError(t, err)

		t.Logf("Create a second APIExport user-1 has no bind on")
		secondExport := &apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: "no-bind-export"},
			Spec: apisv1alpha2.APIExportSpec{
				Resources: []apisv1alpha2.ResourceSchema{
					{
						Name:   "cowboys",
						Group:  "wildwest.dev",
						Schema: "today.cowboys.wildwest.dev",
						Storage: apisv1alpha2.ResourceSchemaStorage{
							CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
						},
					},
				},
			},
		}
		_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), secondExport, metav1.CreateOptions{})
		require.NoError(t, err)

		t.Logf("user-1 attempts to add the second APIExport to defaultAPIBindings — expect denial")
		// Same rationale as the create case: user-1 has no bind on
		// secondExport, so Update MUST fail. Retry only to wait out the
		// transient "not yet ready" admission response.
		expected := fmt.Sprintf("no permission to bind to export %s:%s", providerPathStr, secondExport.Name)
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			latest, getErr := user1KcpClient.Cluster(tenantPath).TenancyV1alpha1().WorkspaceTypes().Get(t.Context(), created.Name, metav1.GetOptions{})
			if getErr != nil {
				return false, getErr.Error()
			}
			latest.Spec.DefaultAPIBindings = []tenancyv1alpha1.APIExportReference{
				{Path: providerPathStr, Export: secondExport.Name},
			}
			latest.Spec.DefaultAPIBindingLifecycle = ptr.To(tenancyv1alpha1.APIBindingLifecycleModeMaintain)
			_, err := user1KcpClient.Cluster(tenantPath).TenancyV1alpha1().WorkspaceTypes().Update(t.Context(), latest, metav1.UpdateOptions{})
			require.Error(t, err, "WorkspaceType update must be denied: user-1 has no bind permission on %s:%s", providerPathStr, secondExport.Name)
			if !strings.Contains(err.Error(), expected) {
				return false, fmt.Sprintf("waiting for deterministic admission error: want %q, got %q", expected, err.Error())
			}
			return true, ""
		}, wait.ForeverTestTimeout, 250*time.Millisecond, "expected denial of WorkspaceType update with deterministic bind error")
	})
}
