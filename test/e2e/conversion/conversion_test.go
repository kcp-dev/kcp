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

package conversion

import (
	"context"
	"embed"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var embeddedResources embed.FS

func TestAPIConversion(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := framework.NewOrganizationFixture(t, server)

	cache := memory.NewMemCacheClient(kcpClusterClient.Cluster(orgPath).Discovery())
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cache)

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "error creating dynamic cluster client")

	t.Logf("Setting up APIResourceSchema, APIConversion, APIExport, and APIBinding for widgets")
	framework.Eventually(t, func() (bool, string) {
		err := helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(orgPath), mapper, nil, "resources.yaml", embeddedResources)
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100.*time.Millisecond, "failed to set up test resources")

	t.Logf("Waiting for initial binding to complete")
	framework.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(orgPath).ApisV1alpha1().APIBindings().Get(ctx, "widgets.example.io", metav1.GetOptions{})
	}, framework.Is(apisv1alpha1.InitialBindingCompleted), "APIBinding never completed its initial binding")

	framework.Eventually(t, func() (success bool, reason string) {
		t.Logf("Resetting the RESTMapper so it can pick up widgets")
		mapper.Reset()

		t.Logf("Creating v1 widget")
		err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(orgPath), mapper, nil, "v1-widget.yaml", embeddedResources)
		if err == nil {
			return true, ""
		}
		return false, err.Error()
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "Unable to create Widget")

	v2GVR := schema.GroupVersionResource{Group: "example.io", Version: "v2", Resource: "widgets"}

	t.Logf("Retrieving the widget as v2")
	v2WidgetClient := dynamicClusterClient.Cluster(orgPath).Resource(v2GVR)
	v2Widget, err := v2WidgetClient.Get(ctx, "bob", metav1.GetOptions{})
	require.NoError(t, err, "error getting v2 widget")

	t.Logf("Ensuring field conversions work")
	requireUnstructuredFieldEqual(t, v2Widget, "Bob", "spec", "name", "first")
	requireUnstructuredFieldEqual(t, v2Widget, "Jones", "spec", "name", "last")
	requireUnstructuredFieldEqual(t, v2Widget, "JONES", "spec", "name", "lastUpper")

	t.Logf("Setting and storing a v2-only field")
	err = unstructured.SetNestedField(v2Widget.Object, "someNewValue", "spec", "someNewField", "hello")
	require.NoError(t, err, "error setting spec.someNewField.hello")
	_, err = v2WidgetClient.Update(ctx, v2Widget, metav1.UpdateOptions{})
	require.NoError(t, err, "error updating v2 widget")

	t.Logf("Getting the v2 widget again to make sure the field was preserved")
	v2Widget, err = v2WidgetClient.Get(ctx, v2Widget.GetName(), metav1.GetOptions{})
	require.NoError(t, err, "error getting v2 widget")
	requireUnstructuredFieldEqual(t, v2Widget, "someNewValue", "spec", "someNewField", "hello")

	t.Logf("Make sure we can create a v2 widget")
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(orgPath), mapper, nil, "v2-widget.yaml", embeddedResources)
	require.NoError(t, err, "error creating v2 widget")

	v1GVR := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	t.Logf("Retrieving the widget as v2")
	v1WidgetClient := dynamicClusterClient.Cluster(orgPath).Resource(v1GVR)
	v1Widget, err := v1WidgetClient.Get(ctx, "alice", metav1.GetOptions{})
	require.NoError(t, err, "error getting v1 widget")

	t.Logf("Ensuring field conversions work")
	requireUnstructuredFieldEqual(t, v1Widget, "Alice", "spec", "firstName")
	requireUnstructuredFieldEqual(t, v1Widget, "Smith", "spec", "lastName")

	t.Logf("Updating v1 names")
	err = unstructured.SetNestedField(v1Widget.Object, "Robot", "spec", "firstName")
	require.NoError(t, err, "error setting spec.firstName")
	err = unstructured.SetNestedField(v1Widget.Object, "Dragon", "spec", "lastName")
	require.NoError(t, err, "error setting spec.lastName")
	_, err = v1WidgetClient.Update(ctx, v1Widget, metav1.UpdateOptions{})
	require.NoError(t, err, "error updating v1 widget")

	t.Logf("Getting the widget as v2 to make sure the names were changed and the v2-only field was preserved")
	v2Widget, err = v2WidgetClient.Get(ctx, v1Widget.GetName(), metav1.GetOptions{})
	require.NoError(t, err, "error getting v2 widget")
	requireUnstructuredFieldEqual(t, v2Widget, "world", "spec", "someNewField", "hello")
	requireUnstructuredFieldEqual(t, v2Widget, "Robot", "spec", "name", "first")
	requireUnstructuredFieldEqual(t, v2Widget, "Dragon", "spec", "name", "last")

	t.Logf("Creating v1 widget without a last name")
	err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(orgPath), mapper, nil, "v1-widget-no-last-name.yaml", embeddedResources)
	require.NoError(t, err, "error creating v1 widget")

	t.Logf("Retrieving the widget as v2")
	v2Widget, err = v2WidgetClient.Get(ctx, "just-bob", metav1.GetOptions{})
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
