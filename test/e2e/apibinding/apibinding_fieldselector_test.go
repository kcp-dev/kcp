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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAPIBindingFieldSelector verifies that selectableFields declared on an
// APIResourceSchema are honored for resources exposed
// through an APIExport and consumed via an APIBinding. It mirrors
// `kubectl get cowboys --field-selector spec.intent=blue` against a bound API.
func TestAPIBindingFieldSelector(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("service-provider"))
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer"))

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	t.Logf("Install cowboys APIResourceSchema (with selectableFields spec.intent) into %q", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys_selectable.yaml", testFiles)
	require.NoError(t, err)

	const exportName = "selectable-cowboys"
	t.Logf("Create an APIExport %q in %q", exportName, providerPath)
	cowboysAPIExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportName,
		},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "wildwest.dev",
					Schema: "selectable.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), cowboysAPIExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create an APIBinding in %q that points to %q from %q", consumerPath, exportName, providerPath)
	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cowboys",
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: exportName,
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	wildwestClusterClient, err := wildwestclientset.NewForConfig(rest.CopyConfig(cfg))
	require.NoError(t, err)
	cowboyClient := wildwestClusterClient.Cluster(consumerPath).WildwestV1alpha1().Cowboys("default")

	t.Logf("Wait for the bound cowboys resource to be servable in consumer workspace %q", consumerPath)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		cowboys, err := cowboyClient.List(t.Context(), metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error listing cowboys: %v", err)
		}
		return len(cowboys.Items) == 0, fmt.Sprintf("expected 0 cowboys to start, got %d", len(cowboys.Items))
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	// Create cowboys with a mix of intents. Only the "blue" ones must be
	// returned when filtering by spec.intent=blue.
	testCowboys := []struct {
		name   string
		intent string
	}{
		{name: "woody", intent: "blue"},
		{name: "buzz", intent: "blue"},
		{name: "jessie", intent: "red"},
		{name: "bullseye", intent: "green"},
	}
	blueCowboys := sets.New[string]()
	for _, tc := range testCowboys {
		if tc.intent == "blue" {
			blueCowboys.Insert(tc.name)
		}
		t.Logf("Create cowboy %q with spec.intent=%q", tc.name, tc.intent)
		_, err := cowboyClient.Create(t.Context(), &wildwestv1alpha1.Cowboy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      tc.name,
				Namespace: "default",
			},
			Spec: wildwestv1alpha1.CowboySpec{
				Intent: tc.intent,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err, "error creating cowboy %q", tc.name)
	}

	t.Logf("List cowboys with --field-selector spec.intent=blue and expect only the blue cowboys")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		filtered, err := cowboyClient.List(t.Context(), metav1.ListOptions{
			FieldSelector: "spec.intent=blue",
		})
		if err != nil {
			return false, fmt.Sprintf("error listing cowboys with field selector spec.intent=blue: %v", err)
		}

		got := sets.New[string]()
		for _, cowboy := range filtered.Items {
			if cowboy.Spec.Intent != "blue" {
				return false, fmt.Sprintf("field selector returned cowboy %q with intent %q, expected only blue", cowboy.Name, cowboy.Spec.Intent)
			}
			got.Insert(cowboy.Name)
		}
		if !got.Equal(blueCowboys) {
			return false, fmt.Sprintf("field selector spec.intent=blue returned %v, expected %v", sets.List(got), sets.List(blueCowboys))
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Verify a field selector on a non-selectable field (spec.intent works, status.result does not) is rejected")
	_, err = cowboyClient.List(t.Context(), metav1.ListOptions{
		FieldSelector: "status.result=won",
	})
	require.Error(t, err, "listing with a field selector on an undeclared selectable field should be rejected")
	require.Contains(t, err.Error(), "not supported", "unexpected error for unsupported field selector: %v", err)
}
