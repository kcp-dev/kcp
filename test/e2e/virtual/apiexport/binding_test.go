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

	org, _ := framework.NewOrganizationFixture(t, server)
	serviceWorkspacePath, _ := framework.NewWorkspaceFixture(t, server, org, framework.WithName("service"))
	restrictedWorkspacePath, _ := framework.NewWorkspaceFixture(t, server, org, framework.WithName("restricted-service"))
	consumerWorkspacePath, consumerWorkspace := framework.NewWorkspaceFixture(t, server, org, framework.WithName("consumer-workspace"))
	cfg := server.BaseConfig(t)

	kubeClient, err := kcpkubernetesclientset.NewForConfig(rest.CopyConfig(cfg))
	require.NoError(t, err)
	kcpClient, err := kcpclientset.NewForConfig(rest.CopyConfig(cfg))
	require.NoError(t, err)
	serviceProviderUser := server.ClientCAUserConfig(t, rest.CopyConfig(cfg), "service-provider")

	framework.AdmitWorkspaceAccess(ctx, t, kubeClient, serviceWorkspacePath, []string{"service-provider"}, nil, true)

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
`, `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: api-manager-role
rules:
- apiGroups:
  - apis.kcp.io
  resources:
  - apiexports/content
  verbs:
  - '*'
  resourceNames:
  - 'api-manager'
`, `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: api-manager-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: api-manager-role
subjects:
- kind: User
  name: service-provider
`))

	require.NoError(t, apply(t, ctx, restrictedWorkspacePath, cfg, `
apiVersion: apis.kcp.io/v1alpha1
kind: APIExport
metadata:
  name: restricted-service
spec: {}
`))

	framework.Eventually(t, func() (bool, string) {
		err := apply(t, ctx, consumerWorkspacePath, cfg, fmt.Sprintf(`
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
			return false, fmt.Sprintf("error creating API binding %v", err.Error())
		}
		return true, ""
	}, wait.ForeverTestTimeout, 1000*time.Millisecond, "waiting on virtual workspace to be ready")

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
			return false, fmt.Sprintf("want %q, got %q", want, got)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 1000*time.Millisecond, "waiting on virtual workspace to be ready")

	require.NoError(t, apply(t, ctx, restrictedWorkspacePath, cfg, `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: anonymous-binder
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
  name: anonymous-binder
subjects:
- kind: User
  name: system:anonymous
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: anonymous-binder
`))

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
