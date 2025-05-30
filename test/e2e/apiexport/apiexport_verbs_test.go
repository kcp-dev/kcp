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

package apiexport

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIExportVerbs(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	orgPath, _ := framework.NewOrganizationFixture(t, server) //nolint:staticcheck // TODO: switch to NewWorkspaceFixture.
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("service-provider-1"))

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	tests := []struct {
		name          string
		verbs         []string
		expectedError string
	}{
		{name: "get-standalone", verbs: []string{"get"}},
		{name: "list-missing-get-and-watch", verbs: []string{"list"}, expectedError: "if 'list' verb is present, the following dependent verbs are missing: 'get', 'watch'"},
		{name: "watch-missing-list-and-get", verbs: []string{"watch"}, expectedError: "if 'watch' verb is present, the following dependent verbs are missing: 'list', 'get'"},
		{name: "delete-standalone", verbs: []string{"delete"}},
		{name: "create-standalone", verbs: []string{"create"}},
		{name: "update-standalone", verbs: []string{"update"}},
		{name: "patch-missing-update-and-create", verbs: []string{"patch"}, expectedError: "if 'patch' verb is present, the following dependent verbs are missing: 'update', 'create'"},
		{name: "patch-missing-create", verbs: []string{"patch", "update"}, expectedError: "if 'patch' verb is present, the following dependent verbs are missing: 'create'"},
		{name: "patch-missing-update", verbs: []string{"patch", "create"}, expectedError: "if 'patch' verb is present, the following dependent verbs are missing: 'update'"},
	}

	for _, test := range tests {
		t.Logf("Create an APIExport %s test in %q", test.name, providerPath)
		testAPIExport := &apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{
				Name: test.name + "-today-cowboys",
			},
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
				PermissionClaims: []apisv1alpha2.PermissionClaim{
					{
						GroupResource: apisv1alpha2.GroupResource{Group: "", Resource: "configmaps"},
						All:           true,
						Verbs:         test.verbs,
					},
				},
			},
		}
		_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(ctx, testAPIExport, metav1.CreateOptions{})
		if test.expectedError != "" {
			require.Error(t, err)
			require.Contains(t, err.Error(), test.expectedError)
		} else {
			require.NoError(t, err)
		}
	}
}
