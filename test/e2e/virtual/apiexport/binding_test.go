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
	"fmt"
	"strings"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"

	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestBinding(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	t.Logf("Creating two service workspaces and a consumer workspace")
	org, _ := framework.NewOrganizationFixture(t, server)
	serviceWorkspacePath, _ := framework.NewWorkspaceFixture(t, server, org, framework.WithName("service"))
	restrictedWorkspacePath, _ := framework.NewWorkspaceFixture(t, server, org, framework.WithName("restricted-service"))
	consumerWorkspacePath, consumerWorkspace := framework.NewWorkspaceFixture(t, server, org, framework.WithName("consumer"))
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
	require.NoError(t, apply(t, ctx, serviceWorkspacePath, cfg, `
apiVersion: apis.kcp.io/v1alpha1
kind: APIExport
metadata:
  name: api-manager
spec:
  permissionClaims:
    - group: "apis.kcp.io"
      resource: "apibindings"
      all: true
`))

	t.Logf("Creating APIExport in restricted workspace without anybody allowed to bind")
	require.NoError(t, apply(t, ctx, restrictedWorkspacePath, cfg, `
apiVersion: apis.kcp.io/v1alpha1
kind: APIExport
metadata:
  name: restricted-service
spec: {}
`))

	t.Logf("Binding to 'api-manager' APIExport succeeds because service-provider user is admin in 'service-provider' workspace")
	framework.Eventually(t, func() (success bool, reason string) {
		err = apply(t, ctx, consumerWorkspacePath, serviceProviderUser, fmt.Sprintf(`
apiVersion: apis.kcp.io/v1alpha1
kind: APIBinding
metadata:
  name: api-manager
spec:
  permissionClaims:
  - group: apis.kcp.io
    resource: apibindings
    state: Accepted
    all: true
  reference:
    export:
      name: api-manager
      path: %v
`, serviceWorkspacePath.String()))
		if err != nil {
			return false, fmt.Sprintf("Waiting on binding 'api-manager' export: %v", err.Error())
		}
		return true, ""
	}, wait.ForeverTestTimeout, 1000*time.Millisecond, "waiting on binding 'api-manager' export")

	t.Logf("Binding directly to 'restricted-service' APIExport should be forbidden")
	framework.Eventually(t, func() (success bool, reason string) {
		err = apply(t, ctx, consumerWorkspacePath, serviceProviderUser, fmt.Sprintf(`
apiVersion: apis.kcp.io/v1alpha1
kind: APIBinding
metadata:
  name: should-fail
spec:
  reference:
    export:
      name: restricted-service
      path: %v`, restrictedWorkspacePath.String()))
		require.Error(t, err)
		want := "unable to create APIBinding: no permission to bind to export"
		if got := err.Error(); !strings.Contains(got, want) {
			return false, fmt.Sprintf("Waiting on binding to 'restricted-service' to fail because of 'bind' permissions: want %q, got %q", want, got)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 1000*time.Millisecond, "waiting on binding to 'restricted-service' to fail because of 'bind' permissions")

	t.Logf("Waiting for 'api-manager' APIExport virtual workspace URL")
	serviceProviderVirtualWorkspaceConfig := rest.CopyConfig(serviceProviderUser)
	framework.Eventually(t, func() (bool, string) {
		apiExport, err := kcpClient.Cluster(serviceWorkspacePath).ApisV1alpha1().APIExports().Get(ctx, "api-manager", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("waiting on apiexport to be available %v", err.Error())
		}
		var found bool
		serviceProviderVirtualWorkspaceConfig.Host, found, err = framework.VirtualWorkspaceURL(ctx, kcpClient, consumerWorkspace, framework.ExportVirtualWorkspaceURLs(apiExport))
		require.NoError(t, err)
		//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", apiExport.Status.VirtualWorkspaces)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting on virtual workspace to be ready")

	t.Logf("Binding to 'restricted-service' APIExport through 'api-manager' APIExport virtual workspace is forbidden")
	framework.Eventually(t, func() (success bool, reason string) {
		err = apply(t, ctx, logicalcluster.Name(consumerWorkspace.Spec.Cluster).Path(), serviceProviderVirtualWorkspaceConfig, fmt.Sprintf(`
apiVersion: apis.kcp.io/v1alpha1
kind: APIBinding
metadata:
  name: should-fail
spec:
  reference:
    export:
      name: restricted-service
      path: %v`, restrictedWorkspacePath.String()))
		require.Error(t, err)
		want := "unable to create APIBinding: no permission to bind to export"
		if got := err.Error(); !strings.Contains(got, want) {
			return false, fmt.Sprintf("Waiting on binding to 'restricted-service' APIExport fail because it is forbidden: want %q, got %q", want, got)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 1000*time.Millisecond, "waiting on binding to 'restricted-service' APIExport to fail because it is forbidden")

	t.Logf("Giving service-provider 'bind' access to 'restricted-service' APIExport")
	require.NoError(t, apply(t, ctx, restrictedWorkspacePath, cfg, `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: restricted-service:bind
rules:
- apiGroups:
  - apis.kcp.io
  resources:
  - apiexports
  verbs:
  - 'bind'
  resourceNames:
  - 'restricted-service'
`, `
kind: ClusterRoleBinding
apiVersion: rbac.authorization.k8s.io/v1
metadata:
  name: service-provider:restricted-service:bind
subjects:
- kind: User
  name: service-provider
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: restricted-service:bind
`))

	t.Logf("Binding to 'restricted-service' APIExport through 'api-manager' APIExport virtual workspace succeeds, proving that the service provider identity is used through the APIExport virtual workspace")
	framework.Eventually(t, func() (bool, string) {
		err := apply(t, ctx, logicalcluster.Name(consumerWorkspace.Spec.Cluster).Path(), serviceProviderVirtualWorkspaceConfig, fmt.Sprintf(`
apiVersion: apis.kcp.io/v1alpha1
kind: APIBinding
metadata:
  name: should-not-fail
spec:
  reference:
    export:
      name: restricted-service
      path: %v`, restrictedWorkspacePath.String()))
		if err != nil {
			return false, fmt.Sprintf("waiting on being able to bind: %v", err.Error())
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "waiting on being able to bind")
}
