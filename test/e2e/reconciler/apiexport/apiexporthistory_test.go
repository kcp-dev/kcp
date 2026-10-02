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

package apiexport

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAPIExportHistoryScope verifies that an APIExport cannot swap an
// APIResourceSchema for one that changes the scope of a group resource it has
// already served, while an otherwise identical swap is still allowed.
func TestAPIExportHistoryScope(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	kcpClusterClient, err := kcpclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	schemas := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIResourceSchemas()
	exports := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports()

	newSchema := func(name string, scope apiextensionsv1.ResourceScope) *apisv1alpha1.APIResourceSchema {
		return &apisv1alpha1.APIResourceSchema{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: apisv1alpha1.APIResourceSchemaSpec{
				Group: "wildwest.dev",
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "sheriffs",
					Singular: "sheriff",
					Kind:     "Sheriff",
					ListKind: "SheriffList",
				},
				Scope: scope,
				Versions: []apisv1alpha1.APIResourceVersion{{
					Name:    "v1alpha1",
					Served:  true,
					Storage: true,
					Schema:  runtime.RawExtension{Raw: []byte(`{"type":"object"}`)},
				}},
			},
		}
	}

	t.Logf("Create a namespaced and a cluster scoped APIResourceSchema for the same group resource")
	_, err = schemas.Create(t.Context(), newSchema("v1.sheriffs.wildwest.dev", apiextensionsv1.NamespaceScoped), metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = schemas.Create(t.Context(), newSchema("v2.sheriffs.wildwest.dev", apiextensionsv1.ClusterScoped), metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = schemas.Create(t.Context(), newSchema("v3.sheriffs.wildwest.dev", apiextensionsv1.NamespaceScoped), metav1.CreateOptions{})
	require.NoError(t, err)

	resourceSchema := func(name string) apisv1alpha2.ResourceSchema {
		return apisv1alpha2.ResourceSchema{
			Name:    "sheriffs",
			Group:   "wildwest.dev",
			Schema:  name,
			Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
		}
	}

	t.Logf("Create an APIExport serving the namespaced schema")
	_, err = exports.Create(t.Context(), &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "wildwest-sheriffs"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{resourceSchema("v1.sheriffs.wildwest.dev")},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	swapSchema := func(name string) error {
		return retry.RetryOnConflict(retry.DefaultRetry, func() error {
			export, err := exports.Get(t.Context(), "wildwest-sheriffs", metav1.GetOptions{})
			if err != nil {
				return err
			}
			export.Spec.Resources = []apisv1alpha2.ResourceSchema{resourceSchema(name)}
			_, err = exports.Update(t.Context(), export, metav1.UpdateOptions{})
			return err
		})
	}

	t.Logf("Wait for the scope to be recorded, swapping in the cluster scoped schema must be rejected")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		err := swapSchema("v2.sheriffs.wildwest.dev")
		if err == nil {
			return false, "cluster scoped schema was accepted"
		}
		if !apierrors.IsForbidden(err) {
			return false, err.Error()
		}
		return strings.Contains(err.Error(), "cannot be served with scope"), err.Error()
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Swapping in another namespaced schema for the same group resource is still allowed")
	require.NoError(t, swapSchema("v3.sheriffs.wildwest.dev"))
}
