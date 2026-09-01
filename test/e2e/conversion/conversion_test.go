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

package conversion

import (
	"embed"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var embeddedResources embed.FS

func TestAPIConversion(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kcp cluster client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "error creating dynamic cluster client")

	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(wsPath).Discovery()))

	t.Logf("Install APIResourceSchema and APIConversion into workspace %q", wsPath)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		err := helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(wsPath), mapper, nil, "resources.yaml", embeddedResources)
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Create APIExport for widgets")
	_, err = kcpClusterClient.Cluster(wsPath).ApisV1alpha2().APIExports().Create(t.Context(), &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets.example.io"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:    "widgets",
					Group:   "example.io",
					Schema:  "rev0002.widgets.example.io",
					Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Create APIBinding for widgets")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(wsPath).ApisV1alpha2().APIBindings().Create(t.Context(), &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "widgets.example.io"},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{Path: wsPath.String(), Name: "widgets.example.io"},
				},
			},
		}, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Waiting for initial binding to complete")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(wsPath).ApisV1alpha2().APIBindings().Get(t.Context(), "widgets.example.io", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		t.Logf("Resetting the RESTMapper so it can pick up widgets")
		mapper.Reset()

		t.Logf("Creating v1 widget")
		err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(wsPath), mapper, nil, "v1-widget.yaml", embeddedResources)
		if err == nil {
			return true, ""
		}
		return false, err.Error()
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	v2GVR := schema.GroupVersionResource{Group: "example.io", Version: "v2", Resource: "widgets"}
	v2WidgetClient := dynamicClusterClient.Cluster(wsPath).Resource(v2GVR)

	t.Logf("Retrieving the widget as v2 and ensuring field conversions work")
	var v2Widget *unstructured.Unstructured
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		var err error
		v2Widget, err = v2WidgetClient.Get(t.Context(), "bob", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		first, ok, err := unstructured.NestedString(v2Widget.Object, "spec", "name", "first")
		if err != nil {
			return false, err.Error()
		}
		if !ok || first != "Bob" {
			return false, fmt.Sprintf("spec.name.first=%q, want Bob (conversion may not be ready yet)", first)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	requireUnstructuredFieldEqual(t, v2Widget, "Bob", "spec", "name", "first")
	requireUnstructuredFieldEqual(t, v2Widget, "Jones", "spec", "name", "last")
	requireUnstructuredFieldEqual(t, v2Widget, "JONES", "spec", "name", "lastUpper")

	t.Logf("Setting and storing a v2-only field")
	err = unstructured.SetNestedField(v2Widget.Object, "someNewValue", "spec", "someNewField", "hello")
	require.NoError(t, err, "error setting spec.someNewField.hello")
	_, err = v2WidgetClient.Update(t.Context(), v2Widget, metav1.UpdateOptions{})
	require.NoError(t, err, "error updating v2 widget")

	t.Logf("Getting the v2 widget again to make sure the field was preserved")
	v2Widget, err = v2WidgetClient.Get(t.Context(), v2Widget.GetName(), metav1.GetOptions{})
	require.NoError(t, err, "error getting v2 widget")
	requireUnstructuredFieldEqual(t, v2Widget, "someNewValue", "spec", "someNewField", "hello")

	t.Logf("Make sure we can create a v2 widget")
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(wsPath), mapper, nil, "v2-widget.yaml", embeddedResources)
	require.NoError(t, err, "error creating v2 widget")

	v1GVR := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	v1WidgetClient := dynamicClusterClient.Cluster(wsPath).Resource(v1GVR)

	t.Logf("Retrieving the widget as v1")
	v1Widget, err := v1WidgetClient.Get(t.Context(), "alice", metav1.GetOptions{})
	require.NoError(t, err, "error getting v1 widget")

	t.Logf("Ensuring field conversions work")
	requireUnstructuredFieldEqual(t, v1Widget, "Alice", "spec", "firstName")
	requireUnstructuredFieldEqual(t, v1Widget, "Smith", "spec", "lastName")

	t.Logf("Updating v1 names")
	err = unstructured.SetNestedField(v1Widget.Object, "Robot", "spec", "firstName")
	require.NoError(t, err, "error setting spec.firstName")
	err = unstructured.SetNestedField(v1Widget.Object, "Dragon", "spec", "lastName")
	require.NoError(t, err, "error setting spec.lastName")
	_, err = v1WidgetClient.Update(t.Context(), v1Widget, metav1.UpdateOptions{})
	require.NoError(t, err, "error updating v1 widget")

	t.Logf("Getting the widget as v2 to make sure the names were changed and the v2-only field was preserved")
	v2Widget, err = v2WidgetClient.Get(t.Context(), v1Widget.GetName(), metav1.GetOptions{})
	require.NoError(t, err, "error getting v2 widget")
	requireUnstructuredFieldEqual(t, v2Widget, "world", "spec", "someNewField", "hello")
	requireUnstructuredFieldEqual(t, v2Widget, "Robot", "spec", "name", "first")
	requireUnstructuredFieldEqual(t, v2Widget, "Dragon", "spec", "name", "last")

	t.Logf("Creating v1 widget without a last name")
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(wsPath), mapper, nil, "v1-widget-no-last-name.yaml", embeddedResources)
	require.NoError(t, err, "error creating v1 widget")

	t.Logf("Retrieving the widget as v2")
	v2Widget, err = v2WidgetClient.Get(t.Context(), "just-bob", metav1.GetOptions{})
	require.NoError(t, err, "error getting v2 widget")

	t.Logf("Ensuring field conversions work when not all fields are present")
	requireUnstructuredFieldEqual(t, v2Widget, "Bob", "spec", "name", "first")
	requireUnstructuredFieldAbsent(t, v2Widget, "spec", "name", "last")
}

func requireUnstructuredFieldEqual(t *testing.T, u *unstructured.Unstructured, expected string, fields ...string) {
	t.Helper()
	actual, exists, err := unstructured.NestedFieldNoCopy(u.Object, fields...)
	require.NoError(t, err, "error getting %s", strings.Join(fields, "."))
	require.True(t, exists, "field %s does not exist", strings.Join(fields, "."))
	require.Empty(t, cmp.Diff(expected, actual), "unexpected value for %s", strings.Join(fields, "."))
}

func requireUnstructuredFieldAbsent(t *testing.T, u *unstructured.Unstructured, fields ...string) {
	t.Helper()
	_, exists, err := unstructured.NestedFieldNoCopy(u.Object, fields...)
	require.NoError(t, err, "error getting %s", strings.Join(fields, "."))
	require.False(t, exists, "field %s should not exist", strings.Join(fields, "."))
}
