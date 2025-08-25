/*
Copyright 2023 The KCP Authors.

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

package apiexport

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestBinding(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	t.Logf("Creating two service workspaces and a consumer workspace")
	org, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	serviceWorkspacePath, _ := kcptesting.NewWorkspaceFixture(t, server, org, kcptesting.WithName("service"))
	restrictedWorkspacePath, _ := kcptesting.NewWorkspaceFixture(t, server, org, kcptesting.WithName("restricted-service"))
	consumerWorkspacePath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, org, kcptesting.WithName("consumer"))
	cfg := server.BaseConfig(t)

	kubeClient, err := kcpkubernetesclientset.NewForConfig(rest.CopyConfig(cfg))
	require.NoError(t, err)
	kcpClient, err := kcpclientset.NewForConfig(rest.CopyConfig(cfg))
	require.NoError(t, err)

	t.Logf("Giving service user admin access to service-provider and consumer workspace")
	serviceProviderUser := server.ClientCAUserConfig(t, rest.CopyConfig(cfg), "service-provider")
	framework.AdmitWorkspaceAccess(ctx, t, kubeClient, serviceWorkspacePath, []string{"service-provider"}, nil, true)
	framework.AdmitWorkspaceAccess(ctx, t, kubeClient, consumerWorkspacePath, []string{"service-provider"}, nil, true)

	t.Logf("Creating 'api-manager' APIExport in service-provider workspace with apibindings permission claim")
	require.NoError(t, apply(t, ctx, serviceWorkspacePath, cfg,
		&apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: "api-manager"},
			Spec: apisv1alpha2.APIExportSpec{
				PermissionClaims: []apisv1alpha2.PermissionClaim{
					{
						GroupResource: apisv1alpha2.GroupResource{
							Group:    "apis.kcp.io",
							Resource: "apibindings",
						},
						Verbs: []string{"*"},
					},
				},
			},
		},
	))
	t.Logf("Creating APIExport in restricted workspace without anybody allowed to bind")
	require.NoError(t, apply(t, ctx, restrictedWorkspacePath, cfg,
		&apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: "restricted-service"},
			Spec:       apisv1alpha2.APIExportSpec{},
		},
	))

	t.Logf("Binding to 'api-manager' APIExport succeeds because service-provider user is admin in 'service-provider' workspace")
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		err = apply(t, ctx, consumerWorkspacePath, serviceProviderUser,
			&apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "api-manager"},
				Spec: apisv1alpha2.APIBindingSpec{
					PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
						{
							ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
								PermissionClaim: apisv1alpha2.PermissionClaim{
									GroupResource: apisv1alpha2.GroupResource{
										Group:    "apis.kcp.io",
										Resource: "apibindings",
									},
									Verbs: []string{"*"},
								},
								Selector: apisv1alpha2.PermissionClaimSelector{
									MatchAll: true,
								},
							},
							State: apisv1alpha2.ClaimAccepted,
						},
					},
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{
							Name: "api-manager",
							Path: serviceWorkspacePath.String(),
						},
					},
				},
			},
		)
		if err != nil {
			return false, fmt.Sprintf("Waiting on binding 'api-manager' export: %v", err.Error())
		}
		return true, ""
	}, wait.ForeverTestTimeout, 1000*time.Millisecond, "waiting on binding 'api-manager' export")

	t.Logf("Binding directly to 'restricted-service' APIExport should be forbidden")
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		err = apply(t, ctx, consumerWorkspacePath, serviceProviderUser,
			&apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "should-fail"},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{
							Name: "restricted-service",
							Path: restrictedWorkspacePath.String(),
						},
					},
				},
			},
		)
		require.Error(t, err)
		want := "unable to create APIBinding: no permission to bind to export"
		if got := err.Error(); !strings.Contains(got, want) {
			return false, fmt.Sprintf("Waiting on binding to 'restricted-service' to fail because of 'bind' permissions: want %q, got %q", want, got)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 1000*time.Millisecond, "waiting on binding to 'restricted-service' to fail because of 'bind' permissions")

	t.Logf("Waiting for 'api-manager' APIExport virtual workspace URL")
	serviceProviderVirtualWorkspaceConfig := rest.CopyConfig(serviceProviderUser)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClient.Cluster(serviceWorkspacePath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, "api-manager", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("waiting on apiexport to be available %v", err.Error())
		}
		var found bool
		serviceProviderVirtualWorkspaceConfig.Host, found, err = framework.VirtualWorkspaceURL(ctx, kcpClient, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
		require.NoError(t, err)
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExportEndpointSlice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting on virtual workspace to be ready")

	t.Logf("Binding to 'restricted-service' APIExport through 'api-manager' APIExport virtual workspace is forbidden")
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		err = apply(t, ctx, logicalcluster.Name(consumerWorkspace.Spec.Cluster).Path(), serviceProviderVirtualWorkspaceConfig,
			&apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "should-fail"},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{
							Name: "restricted-service",
							Path: restrictedWorkspacePath.String(),
						},
					},
				},
			},
		)
		require.Error(t, err)
		want := "unable to create APIBinding: no permission to bind to export"
		if got := err.Error(); !strings.Contains(got, want) {
			return false, fmt.Sprintf("Waiting on binding to 'restricted-service' APIExport fail because it is forbidden: want %q, got %q", want, got)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 1000*time.Millisecond, "waiting on binding to 'restricted-service' APIExport to fail because it is forbidden")

	t.Logf("Giving service-provider 'bind' access to 'restricted-service' APIExport")
	require.NoError(t, apply(t, ctx, restrictedWorkspacePath, cfg,
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: "restricted-service:bind"},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{"apis.kcp.io"},
					Resources: []string{"apiexports"},
					Verbs:     []string{"bind"},
					ResourceNames: []string{
						"restricted-service",
					},
				},
			},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "service-provider:restricted-service:bind"},
			Subjects: []rbacv1.Subject{
				{
					Kind: "User",
					Name: "service-provider",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "restricted-service:bind",
			},
		},
	))

	t.Logf("Binding to 'restricted-service' APIExport through 'api-manager' APIExport virtual workspace succeeds, proving that the service provider identity is used through the APIExport virtual workspace")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		err := apply(t, ctx, logicalcluster.Name(consumerWorkspace.Spec.Cluster).Path(), serviceProviderVirtualWorkspaceConfig,
			&apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "should-not-fail"},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{
							Name: "restricted-service",
							Path: restrictedWorkspacePath.String(),
						},
					},
				},
			},
		)
		if err != nil {
			return false, fmt.Sprintf("waiting on being able to bind: %v", err.Error())
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting on being able to bind")
}

func TestAPIBindingPermissionClaimsVerbs(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, providerPath, kcpClusterClient, "wild.wild.west", "board the wanderer")

	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid), "could not wait for APIExport to be valid with identity hash")

	sheriffExport, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
	require.NoError(t, err)
	identityHash := sheriffExport.Status.IdentityHash

	t.Logf("Found identity hash: %v", identityHash)
	apifixtures.BindToExport(ctx, t, providerPath, "wild.wild.west", consumerPath, kcpClusterClient)

	pcModifiers := []func([]apisv1alpha2.PermissionClaim){
		readonlyVerbsForResource("", "secrets", ""),
		readwriteVerbsForResource("", "configmaps", ""),
	}
	permissionClaims := defaultPermissionsClaims(identityHash, pcModifiers...)

	t.Logf("set up service provider with permission claims")
	setUpServiceProvider(ctx, t, dynamicClusterClient, kcpClusterClient, false, providerPath, cfg, permissionClaims)

	t.Logf("Set up binding")
	bindConsumerToProvider(ctx, t, consumerPath, providerPath, kcpClusterClient, cfg, permissionClaimsToAcceptable(permissionClaims)...)

	t.Logf("Validate that the permission claims are valid")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.PermissionClaimsValid), "unable to see valid claims")
	binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	if !reflect.DeepEqual(permissionClaims, binding.Status.ExportPermissionClaims) {
		require.Emptyf(t, cmp.Diff(permissionClaims, binding.Status.ExportPermissionClaims), "ExportPermissionClaims incorrect")
	}

	t.Logf("Validate that the permission claims were all applied")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.PermissionClaimsApplied), "unable to see claims applied")

	t.Logf("Waiting for APIExport to have a virtual workspace URL for the bound workspace %q", consumerPath)
	apiExportVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, "today-cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		var found bool
		apiExportVWCfg.Host, found, err = framework.VirtualWorkspaceURL(ctx, kcpClusterClient, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
		require.NoError(t, err)
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExportEndpointSlice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err, "error building kube client for %q", consumerPath)

	t.Logf("Make sure configmaps list shows only one item") // there is default kubernetes configmap with the CA certificate
	// It takes some time for the permissions to be applied in the sharded setup, so we need to retry a couple of times
	var configmaps *corev1.ConfigMapList
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		var cmErr error
		configmaps, cmErr = kubeClusterClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
		if cmErr != nil {
			return false, cmErr.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error listing configmaps")
	require.Len(t, configmaps.Items, 1, "expected 1 configmaps inside %q", consumerPath)

	t.Logf("Create a configmap in consumer workspace %q", consumerPath)
	configMapName := fmt.Sprintf("configmap-%s", consumerPath.Base())
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: "default",
		},
	}
	_, err = kubeClusterClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Create(ctx, configMap, metav1.CreateOptions{})
	require.NoError(t, err, "error creating configmap")

	t.Logf("Make sure secrets list shows nothing to start")
	secrets, err := kubeClusterClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing secrets inside %q", consumerPath)
	require.Zero(t, len(secrets.Items), "expected 0 secrets inside %q", consumerPath)

	t.Logf("Create a secret in consumer workspace %q before allowing create", consumerPath)
	secretName := fmt.Sprintf("secret-%s", consumerPath.Base())
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: "default",
		},
	}

	_, err = kubeClusterClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").Create(ctx, secret, metav1.CreateOptions{})
	require.Error(t, err, "error creating secret expected")

	// Allow read-write on Secrets, and change ConfigMaps to read-only
	pcModifiers = []func([]apisv1alpha2.PermissionClaim){
		readwriteVerbsForResource("", "secrets", ""),
		readonlyVerbsForResource("", "configmaps", ""),
	}
	permissionClaims = defaultPermissionsClaims(identityHash, pcModifiers...)

	t.Logf("update permission claims verbs in APIExport")
	// have to use eventually because controllers may be modifying the APIBinding
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		export, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		export.Spec.PermissionClaims = permissionClaims
		_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Update(ctx, export, metav1.UpdateOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error apiexport updating to new allowed verbs")

	t.Logf("update permission claims verbs in APIBinding")
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		binding.Spec.PermissionClaims = permissionClaimsToAcceptable(permissionClaims)
		_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Update(ctx, binding, metav1.UpdateOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error apibinding updating to new allowed verbs")

	t.Logf("Create a secret in consumer workspace %q after allowing create", consumerPath)
	// have to use eventually because updating permission claims might take a while to get into the effect
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		_, err = kubeClusterClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error creating a secret")

	secrets, err = kubeClusterClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing secrets inside %q", consumerPath)
	require.Len(t, secrets.Items, 1, "expected 1 secrets inside %q", consumerPath)

	_, err = kubeClusterClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").Get(ctx, secretName, metav1.GetOptions{})
	require.NoError(t, err, "error getting secret")

	t.Logf("List configmaps after dropping write permissions")
	configmaps, err = kubeClusterClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing configmaps inside %q", consumerPath)
	require.Len(t, configmaps.Items, 2, "expected 2 configmaps inside %q", consumerPath)

	t.Logf("Create a configmap in consumer workspace %q after dropping write permissions", consumerPath)
	configMapName = fmt.Sprintf("configmap-%s-2", consumerPath.Base())
	configMap = &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: "default",
		},
	}
	_, err = kubeClusterClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Create(ctx, configMap, metav1.CreateOptions{})
	require.Error(t, err, "error creating configmap expected")
}

func TestAPIBindingPermissionClaimsSSA(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, providerPath, kcpClusterClient, "wild.wild.west", "board the wanderer")

	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid), "could not wait for APIExport to be valid with identity hash")

	sheriffExport, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
	require.NoError(t, err)
	identityHash := sheriffExport.Status.IdentityHash

	t.Logf("Found identity hash: %v", identityHash)
	apifixtures.BindToExport(ctx, t, providerPath, "wild.wild.west", consumerPath, kcpClusterClient)

	pcModifiers := []func([]apisv1alpha2.PermissionClaim){
		verbsForResource("", "configmaps", "", []string{"get", "patch"}),
	}
	permissionClaims := defaultPermissionsClaims(identityHash, pcModifiers...)

	t.Logf("set up service provider with permission claims")
	setUpServiceProvider(ctx, t, dynamicClusterClient, kcpClusterClient, false, providerPath, cfg, permissionClaims)

	t.Logf("Set up binding")
	bindConsumerToProvider(ctx, t, consumerPath, providerPath, kcpClusterClient, cfg, permissionClaimsToAcceptable(permissionClaims)...)

	t.Logf("Validate that the permission claims are valid")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.PermissionClaimsValid), "unable to see valid claims")
	binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	if !reflect.DeepEqual(permissionClaims, binding.Status.ExportPermissionClaims) {
		require.Emptyf(t, cmp.Diff(permissionClaims, binding.Status.ExportPermissionClaims), "ExportPermissionClaims incorrect")
	}

	t.Logf("Validate that the permission claims were all applied")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.PermissionClaimsApplied), "unable to see claims applied")

	t.Logf("Waiting for APIExport to have a virtual workspace URL for the bound workspace %q", consumerPath)
	apiExportVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, "today-cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		var found bool
		apiExportVWCfg.Host, found, err = framework.VirtualWorkspaceURL(ctx, kcpClusterClient, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
		require.NoError(t, err)
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExportEndpointSlice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err, "error building kube client for %q", consumerPath)

	// ConfigMap's name used through the test
	configMapName := fmt.Sprintf("configmap-%s", consumerPath.Base())

	t.Logf("Ensure the ConfigMap does not exist before server-side applying it")
	_, err = kubeClusterClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Get(ctx, configMapName, metav1.GetOptions{})
	require.True(t, kerrors.IsNotFound(err), "found configmap, but expected not found")

	t.Logf("Ensure sever-side applying the ConfigMap fails without the create permission")
	// We'll send the whole ConfigMap as a JSON with the PATCH request
	configMap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: "default",
		},
	}
	configMapJson, err := json.Marshal(configMap)
	require.NoError(t, err)

	_, err = kubeClusterClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Patch(ctx, configMapName, types.ApplyYAMLPatchType, configMapJson, metav1.PatchOptions{
		FieldManager: "test-manager",
		// FieldValidation depends on OpenAPI v2 which we do not support
		FieldValidation: "Ignore",
	})
	require.True(t, kerrors.IsForbidden(err), "expected forbidden configmap server-side apply")

	// Allow patch and create on ConfigMaps
	pcModifiers = []func([]apisv1alpha2.PermissionClaim){
		verbsForResource("", "configmaps", "", []string{"get", "patch", "create"}),
	}
	permissionClaims = defaultPermissionsClaims(identityHash, pcModifiers...)

	t.Logf("update permission claims verbs in APIExport")
	// have to use eventually because controllers may be modifying the APIBinding
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		export, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		export.Spec.PermissionClaims = permissionClaims
		_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Update(ctx, export, metav1.UpdateOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error apiexport updating to new allowed verbs")

	t.Logf("update permission claims verbs in APIBinding")
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		binding.Spec.PermissionClaims = permissionClaimsToAcceptable(permissionClaims)
		_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Update(ctx, binding, metav1.UpdateOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error apibinding updating to new allowed verbs")

	t.Logf("Server-side apply a ConfigMap in consumer workspace %q after allowing create", consumerPath)
	// have to use eventually because updating permission claims might take a while to get into the effect
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		configMap, err = kubeClusterClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Patch(ctx, configMapName, types.ApplyYAMLPatchType, configMapJson, metav1.PatchOptions{
			FieldManager:    "test-manager",
			FieldValidation: "Ignore",
		})
		if err != nil {
			return false, err.Error()
		}
		require.NotNil(t, configMap)
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error server-side applying configmap")

	t.Logf("Ensure the ConfigMap exists after server-side applying it")
	configMap, err = kubeClusterClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Get(ctx, configMapName, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, configMap)
}

func TestAPIBindingPermissionClaimsSelectors(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := framework.NewOrganizationFixture(t, server) //nolint:staticcheck // TODO: switch to NewWorkspaceFixture.
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWorkspace := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerClusterName := logicalcluster.Name(consumerWorkspace.Spec.Cluster)

	cfg := server.BaseConfig(t)

	kubeClient, err := kcpkubernetesclientset.NewForConfig(rest.CopyConfig(cfg))
	require.NoError(t, err, "failed to construct kube client for server")

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, providerPath, kcpClusterClient, "wild.wild.west", "board the wanderer")

	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid), "could not wait for APIExport to be valid with identity hash")

	sheriffExport, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(ctx, "wild.wild.west", metav1.GetOptions{})
	require.NoError(t, err)
	identityHash := sheriffExport.Status.IdentityHash

	t.Logf("Found identity hash: %v", identityHash)
	apifixtures.BindToExport(ctx, t, providerPath, "wild.wild.west", consumerPath, kcpClusterClient)
	permissionClaims := defaultPermissionsClaims(identityHash)

	t.Logf("set up service provider with permission claims")
	setUpServiceProvider(ctx, t, dynamicClusterClient, kcpClusterClient, false, providerPath, cfg, permissionClaims)

	apcModifiers := []func([]apisv1alpha2.AcceptablePermissionClaim){
		selectorForResource("", "configmaps", "", apisv1alpha2.PermissionClaimSelector{
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"test": "test",
				},
			},
		}),
		selectorForResource("", "secrets", "", apisv1alpha2.PermissionClaimSelector{
			LabelSelector: metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "test",
						Operator: metav1.LabelSelectorOpIn,
						Values:   []string{"test1", "test2"},
					},
				},
			},
		}),
	}

	t.Logf("Set up binding")
	bindConsumerToProvider(ctx, t, consumerPath, providerPath, kcpClusterClient, cfg, permissionClaimsToAcceptable(permissionClaims, apcModifiers...)...)

	t.Logf("Validate that the permission claims are valid")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.PermissionClaimsValid), "unable to see valid claims")
	binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	if !reflect.DeepEqual(permissionClaims, binding.Status.ExportPermissionClaims) {
		require.Emptyf(t, cmp.Diff(permissionClaims, binding.Status.ExportPermissionClaims), "ExportPermissionClaims incorrect")
	}

	t.Logf("Validate that the permission claims were all applied")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, "cowboys", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.PermissionClaimsApplied), "unable to see claims applied")

	t.Logf("Waiting for APIExport to have a virtual workspace URL for the bound workspace %q", consumerPath)
	apiExportVWCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		apiExportEndpointSlice, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, "today-cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		var found bool
		apiExportVWCfg.Host, found, err = framework.VirtualWorkspaceURL(ctx, kcpClusterClient, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExportEndpointSlice))
		require.NoError(t, err)
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExportEndpointSlice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	vwKubeClient, err := kcpkubernetesclientset.NewForConfig(apiExportVWCfg)
	require.NoError(t, err, "error building kube client for %q", consumerPath)

	t.Logf("Create test resources in the consumer workspace")
	configMap1 := testConfigMapWithLabels(1, map[string]string{"env": "random1"})
	_, err = kubeClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Create(ctx, configMap1, metav1.CreateOptions{})
	require.NoError(t, err, "error creating configmap 1")

	configMap2 := testConfigMapWithLabels(2, map[string]string{"test": "test", "env": "random2"})
	_, err = kubeClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Create(ctx, configMap2, metav1.CreateOptions{})
	require.NoError(t, err, "error creating configmap 2")

	secret1 := testSecretMapWithLabels(1, map[string]string{"test": "test", "env": "random1"})
	_, err = kubeClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").Create(ctx, secret1, metav1.CreateOptions{})
	require.NoError(t, err, "error creating secret 1")

	secret2 := testSecretMapWithLabels(2, map[string]string{"test": "test1", "env": "random2"})
	_, err = kubeClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").Create(ctx, secret2, metav1.CreateOptions{})
	require.NoError(t, err, "error creating secret 2")

	secret3 := testSecretMapWithLabels(3, map[string]string{"test": "test2", "env": "prod3"})
	_, err = kubeClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").Create(ctx, secret3, metav1.CreateOptions{})
	require.NoError(t, err, "error creating secret 3")

	t.Logf("Make sure configmaps list shows only one item") // test-configmap-2 is expected
	// It takes some time for the permissions to be applied in the sharded setup, so we need to retry a couple of times
	var configmaps *corev1.ConfigMapList
	kcptestinghelpers.Eventually(t, func() (success bool, reason string) {
		var cmErr error
		configmaps, cmErr = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
		if cmErr != nil {
			return false, cmErr.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error listing configmaps")
	require.Len(t, configmaps.Items, 1, "expected 1 configmaps") // test-configmap-2
	require.Equal(t, configMap2.Name, configmaps.Items[0].Name, "expected to get configmap test-configmap-2")

	secrets, err := vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing secrets in the virtual workspace")
	require.Len(t, secrets.Items, 2, "expected 2 secrets") // test-secret-2 and test-secret-3
	for _, secret := range secrets.Items {                 // we do this because we don't know if secrets are sorted in the response
		if secret.Name != secret2.Name && secret.Name != secret3.Name {
			require.Fail(t, "unexpected secret found", secret.Name)
		}
	}

	t.Logf("Create a configmap without labels in consumer workspace %q", consumerPath)
	configMap3 := testConfigMapWithLabels(3, nil)
	_, err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Create(ctx, configMap3, metav1.CreateOptions{})
	require.NoError(t, err, "error creating configmap")

	configMap3, err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Get(ctx, configMap3.Name, metav1.GetOptions{})
	require.NoError(t, err, "error getting created configmap")
	require.GreaterOrEqual(t, len(configMap3.Labels), 2, "expected at least two labels on configmap") // claimed + test labels
	require.Equal(t, "test", configMap3.Labels["test"], "expected label test to equal test")

	t.Logf("Create a secret with no required labels in consumer workspace %q", consumerPath)
	secret4 := testSecretMapWithLabels(4, map[string]string{"env": "prod"})
	_, err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").Create(ctx, secret4, metav1.CreateOptions{})
	require.Error(t, err, "expected error creating secret without labels due to matchExpressions")

	t.Logf("Create a secret with labels in consumer workspace %q", consumerPath)
	secret4.Labels = map[string]string{"test": "test1"}
	secret4, err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").Create(ctx, secret4, metav1.CreateOptions{})
	require.NoError(t, err, "error creating test secret")

	t.Logf("Verifying the label is correctly persisted on a created secret %q", consumerPath)
	secret4, err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").Get(ctx, secret4.Name, metav1.GetOptions{})
	require.NoError(t, err, "error getting created secret")
	require.GreaterOrEqual(t, len(secret4.Labels), 2, "expected at least two labels on secret") // claimed + test
	require.Equal(t, "test1", secret4.Labels["test"], "expected label test to equal test1")

	t.Logf("Delete a configmap without label selector")
	err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Delete(ctx, configMap1.Name, metav1.DeleteOptions{})
	require.Error(t, err, "expected error deleting configmap without label")

	t.Logf("Delete a configmap with label selector")
	err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Delete(ctx, configMap2.Name, metav1.DeleteOptions{})
	require.NoError(t, err, "error deleting configmap with label")

	t.Logf("Delete a secret without label selector")
	err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").Delete(ctx, secret1.Name, metav1.DeleteOptions{})
	require.Error(t, err, "expected error deleting secret without label")

	t.Logf("Delete a secret with label selector")
	err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").Delete(ctx, secret2.Name, metav1.DeleteOptions{})
	require.NoError(t, err, "error deleting secret with label")

	t.Logf("Update configmap with a new label")
	configMap3.Labels["tier"] = "random1"
	configMap3, err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Update(ctx, configMap3, metav1.UpdateOptions{})
	require.NoError(t, err, "error updating configmap with new label")
	require.Equal(t, "random1", configMap3.Labels["tier"], "expected label tier to equal random1")
	require.Equal(t, "test", configMap3.Labels["test"], "expected label test to equal test")

	t.Logf("Update configmap to drop the selector label") // no-op because of mutation
	delete(configMap3.Labels, "test")
	configMap3, err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Update(ctx, configMap3, metav1.UpdateOptions{})
	require.NoError(t, err, "error updating configmap with new label")
	require.Equal(t, "test", configMap3.Labels["test"], "expected label test to equal test")

	t.Logf("Update configmap to modifty the selector label") // error because selector label is a protected label
	configMap3.Labels["test"] = "test1"
	_, err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").Update(ctx, configMap3, metav1.UpdateOptions{})
	require.Error(t, err, "expected error updating configmap with new label")

	t.Logf("Update secret with a new label")
	secret4.Labels["tier"] = "random1"
	secret4, err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").Update(ctx, secret4, metav1.UpdateOptions{})
	require.NoError(t, err, "error updating secret with new label")
	require.Equal(t, "random1", secret4.Labels["tier"], "expected label tier to equal random1")
	require.Equal(t, "test1", secret4.Labels["test"], "expected label test to equal test1")

	t.Logf("Update secret to drop the selector label") // error because no mutation for matchExpressions
	delete(secret4.Labels, "test")
	_, err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").Update(ctx, secret4, metav1.UpdateOptions{})
	require.Error(t, err, "error updating secret with new label")

	t.Logf("Update secret to modifty the selector label") // error because selector label is a protected label
	secret4.Labels["test"] = "test3"
	_, err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").Update(ctx, secret4, metav1.UpdateOptions{})
	require.Error(t, err, "expected error updating secret with new label")

	t.Logf("List configmaps after operations via VW")
	configmaps, err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing configmaps inside %q", consumerPath)
	require.Len(t, configmaps.Items, 1, "expected 1 configmaps inside %q", consumerPath) // 1 test configmap with label

	t.Logf("List configmaps after operations via workspace")
	configmaps, err = kubeClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing configmaps inside %q", consumerPath)
	require.Len(t, configmaps.Items, 3, "expected 3 configmaps inside %q", consumerPath) // default kube + 1 test configmap without label + 1 test configmaps with label

	t.Logf("List secrets after secrets via VW")
	secrets, err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing secrets inside %q", consumerPath)
	require.Len(t, secrets.Items, 2, "expected 2 secrets inside %q", consumerPath) // 2 test secrets with labels

	t.Logf("List secrets after secrets via workspace")
	secrets, err = kubeClient.Cluster(consumerClusterName.Path()).CoreV1().Secrets("default").List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing secrets inside %q", consumerPath)
	require.Len(t, secrets.Items, 3, "expected 3 secrets inside %q", consumerPath) // 3 test secrets (one without label)

	t.Logf("Delete all configmaps from the VW")
	err = vwKubeClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{})
	require.NoError(t, err, "error deleting all configmaps")

	t.Logf("List configmaps after delete all via workspace")
	configmaps, err = kubeClient.Cluster(consumerClusterName.Path()).CoreV1().ConfigMaps("default").List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing configmaps inside %q", consumerPath)
	require.Len(t, configmaps.Items, 2, "expected 2 configmaps inside %q", consumerPath) // kube default + 1 test configmap without label
}

func readonlyVerbsForResource(group, resource, identityHash string) func([]apisv1alpha2.PermissionClaim) {
	return func(pcs []apisv1alpha2.PermissionClaim) {
		for i, pc := range pcs {
			if pc.GroupResource.Group == group && pc.GroupResource.Resource == resource && pc.IdentityHash == identityHash {
				pcs[i].Verbs = []string{"list", "get"}
			}
		}
	}
}

func readwriteVerbsForResource(group, resource, identityHash string) func([]apisv1alpha2.PermissionClaim) {
	return func(pcs []apisv1alpha2.PermissionClaim) {
		for i, pc := range pcs {
			if pc.GroupResource.Group == group && pc.GroupResource.Resource == resource && pc.IdentityHash == identityHash {
				pcs[i].Verbs = []string{"list", "get", "create", "update", "patch"}
			}
		}
	}
}

func verbsForResource(group, resource, identityHash string, verbs []string) func([]apisv1alpha2.PermissionClaim) {
	return func(pcs []apisv1alpha2.PermissionClaim) {
		for i, pc := range pcs {
			if pc.GroupResource.Group == group && pc.GroupResource.Resource == resource && pc.IdentityHash == identityHash {
				pcs[i].Verbs = verbs
			}
		}
	}
}

func selectorForResource(group, resource, identityHash string, selector apisv1alpha2.PermissionClaimSelector) func([]apisv1alpha2.AcceptablePermissionClaim) {
	return func(pcs []apisv1alpha2.AcceptablePermissionClaim) {
		for i, pc := range pcs {
			if pc.GroupResource.Group == group && pc.GroupResource.Resource == resource && pc.IdentityHash == identityHash {
				pcs[i].Selector = selector
			}
		}
	}
}

func testConfigMapWithLabels(num int, labels map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("test-configmap-%d", num),
			Namespace: "default",
			Labels:    labels,
		},
		Data: map[string]string{"test": "test"},
	}
}

func testSecretMapWithLabels(num int, labels map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("test-secret-%d", num),
			Namespace: "default",
			Labels:    labels,
		},
		Data: map[string][]byte{"test": []byte("test")},
	}
}

func permissionClaimsToAcceptable(permissionClaims []apisv1alpha2.PermissionClaim, modifiers ...func([]apisv1alpha2.AcceptablePermissionClaim)) []apisv1alpha2.AcceptablePermissionClaim {
	acceptablePermissionClaims := []apisv1alpha2.AcceptablePermissionClaim{}
	for _, pc := range permissionClaims {
		acceptablePermissionClaims = append(acceptablePermissionClaims, apisv1alpha2.AcceptablePermissionClaim{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: pc,
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		})
	}

	for _, modifier := range modifiers {
		modifier(acceptablePermissionClaims)
	}

	return acceptablePermissionClaims
}
