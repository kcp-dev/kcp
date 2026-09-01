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

package apibinding

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/config/helpers"
	webhookserver "github.com/kcp-dev/kcp/test/e2e/fixtures/webhook"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPILifecycleWithVersionMigration(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client")

	// ── Phase 1: install v1alpha1 schema + export ──────────────────────────

	t.Logf("Installing v1alpha1 cowboys schema into provider workspace %q", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Creating APIExport lifecycle-cowboys in provider workspace %q", providerPath)
	cowboysExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "lifecycle-cowboys"},
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
	cowboysExport, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), cowboysExport, metav1.CreateOptions{})
	require.NoError(t, err)

	// ── Phase 2: consumer binds and creates a v1alpha1 object ─────────────

	t.Logf("Creating APIBinding in consumer workspace %q pointing to lifecycle-cowboys", consumerPath)
	apiBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "cowboys"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: "lifecycle-cowboys",
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), apiBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	cowboysV1GVR := schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "cowboys"}

	t.Logf("Waiting for v1alpha1 cowboys to be served in consumer workspace %q", consumerPath)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := dynamicClusterClient.
			Cluster(consumerPath).
			Resource(cowboysV1GVR).
			Namespace("default").
			List(t.Context(), metav1.ListOptions{})
		return err == nil, fmt.Sprintf("cowboys v1alpha1 not yet served: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Creating v1alpha1 cowboy legacy-outlaw in consumer workspace %q", consumerPath)
	v1Cowboy := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "wildwest.dev/v1alpha1",
			"kind":       "Cowboy",
			"metadata": map[string]interface{}{
				"name":      "legacy-outlaw",
				"namespace": "default",
			},
			"spec": map[string]interface{}{
				"intent": "rob the stage coach",
			},
		},
	}
	_, err = dynamicClusterClient.
		Cluster(consumerPath).
		Resource(cowboysV1GVR).
		Namespace("default").
		Create(t.Context(), v1Cowboy, metav1.CreateOptions{})
	require.NoError(t, err)

	// ── Phase 3: launch conversion webhook ────────────────────────────────

	t.Logf("Starting cowboy conversion webhook server")
	webhookPort, err := kcptestingserver.GetFreePort(t)
	require.NoError(t, err, "failed to get free port for conversion webhook")

	dirPath := filepath.Dir(server.KubeconfigPath())
	convWebhook := &webhookserver.ConversionWebhookServer{
		ConvertFn: func(obj map[string]interface{}, desiredAPIVersion string) (map[string]interface{}, error) {
			// Deep-copy via JSON round-trip to avoid mutating the original.
			raw, err := json.Marshal(obj)
			if err != nil {
				return nil, fmt.Errorf("marshaling object: %w", err)
			}
			var out map[string]interface{}
			if err := json.Unmarshal(raw, &out); err != nil {
				return nil, fmt.Errorf("unmarshaling object copy: %w", err)
			}

			out["apiVersion"] = desiredAPIVersion

			spec, _ := out["spec"].(map[string]interface{})
			if spec == nil {
				spec = map[string]interface{}{}
			}

			switch desiredAPIVersion {
			case "wildwest.dev/v1alpha2":
				if intent, ok := spec["intent"].(string); ok {
					spec["goal"] = intent
					delete(spec, "intent")
				}
			case "wildwest.dev/v1alpha1":
				if goal, ok := spec["goal"].(string); ok {
					spec["intent"] = goal
					delete(spec, "goal")
				}
			default:
				return nil, fmt.Errorf("unsupported target API version %q", desiredAPIVersion)
			}

			out["spec"] = spec
			return out, nil
		},
	}
	convWebhook.StartTLS(t,
		filepath.Join(dirPath, "apiserver.crt"),
		filepath.Join(dirPath, "apiserver.key"),
		cfg.Host,
		webhookPort,
	)

	// ── Phase 4: publish a two-version schema with webhook conversion ─────

	t.Logf("Creating v2 cowboys APIResourceSchema (v1alpha1+v1alpha2) in provider workspace %q", providerPath)

	v1alpha1Schema, err := json.Marshal(&apiextensionsv1.JSONSchemaProps{
		Type:        "object",
		Description: "Cowboy is part of the wild west (v1alpha1)",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"apiVersion": {Type: "string"},
			"kind":       {Type: "string"},
			"metadata":   {Type: "object"},
			"spec": {
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"intent": {Type: "string"},
				},
			},
			"status": {
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"result": {Type: "string"},
				},
			},
		},
	})
	require.NoError(t, err, "marshalling schema should succeed")

	v1alpha2Schema, err := json.Marshal(&apiextensionsv1.JSONSchemaProps{
		Type:        "object",
		Description: "Cowboy is part of the wild west (v1alpha2)",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"apiVersion": {Type: "string"},
			"kind":       {Type: "string"},
			"metadata":   {Type: "object"},
			"spec": {
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"goal": {Type: "string"},
				},
			},
			"status": {
				Type: "object",
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"result": {Type: "string"},
				},
			},
		},
	})
	require.NoError(t, err, "marshalling schema should succeed")

	v2APIResourceSchema := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{Name: "v2.cowboys.wildwest.dev"},
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: "wildwest.dev",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:     "Cowboy",
				ListKind: "CowboyList",
				Plural:   "cowboys",
				Singular: "cowboy",
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Versions: []apisv1alpha1.APIResourceVersion{
				{
					Name:    "v1alpha1",
					Served:  true,
					Storage: false,
					Schema:  runtime.RawExtension{Raw: v1alpha1Schema},
					Subresources: apiextensionsv1.CustomResourceSubresources{
						Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
					},
				},
				{
					Name:    "v1alpha2",
					Served:  true,
					Storage: true,
					Schema:  runtime.RawExtension{Raw: v1alpha2Schema},
					Subresources: apiextensionsv1.CustomResourceSubresources{
						Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
					},
				},
			},
			Conversion: &apisv1alpha1.CustomResourceConversion{
				Strategy: apisv1alpha1.ConversionStrategyType("Webhook"),
				Webhook: &apisv1alpha1.WebhookConversion{
					ConversionReviewVersions: []string{"v1"},
					ClientConfig: &apisv1alpha1.WebhookClientConfig{
						URL:      convWebhook.GetURL(),
						CABundle: cfg.CAData,
					},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIResourceSchemas().Create(t.Context(), v2APIResourceSchema, metav1.CreateOptions{})
	require.NoError(t, err)

	calls := convWebhook.Calls()
	require.Equal(t, 0, calls, "there should have be no calls to the conversion webhook now")

	t.Logf("Updating APIExport lifecycle-cowboys to reference v2.cowboys.wildwest.dev")
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		cowboysExport, err = kcpClusterClient.
			Cluster(providerPath).
			ApisV1alpha2().
			APIExports().
			Get(t.Context(), "lifecycle-cowboys", metav1.GetOptions{})
		require.NoError(t, err)
		cowboysExport.Spec.Resources[0].Schema = "v2.cowboys.wildwest.dev"
		_, err = kcpClusterClient.
			Cluster(providerPath).
			ApisV1alpha2().
			APIExports().
			Update(t.Context(), cowboysExport, metav1.UpdateOptions{})
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "Updating the APIExport with v1alpha2 schema should succeed")

	// ── Phase 5: verify new version is served and conversion works ─────────

	cowboysV2GVR := schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha2", Resource: "cowboys"}

	t.Logf("Waiting for v1alpha2 cowboys to be served in consumer workspace %q", consumerPath)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := dynamicClusterClient.
			Cluster(consumerPath).
			Resource(cowboysV2GVR).
			Namespace("default").
			List(t.Context(), metav1.ListOptions{})
		return err == nil, fmt.Sprintf("cowboys v1alpha2 not yet served: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	ensureCallReceived(t, &calls, convWebhook.Calls())

	t.Logf("Getting legacy v1alpha1 cowboy via v1alpha2 API – webhook should convert spec.intent→spec.goal")
	var convertedCowboy *unstructured.Unstructured
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		convertedCowboy, err = dynamicClusterClient.
			Cluster(consumerPath).
			Resource(cowboysV2GVR).
			Namespace("default").
			Get(t.Context(), "legacy-outlaw", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting cowboy via v1alpha2: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	ensureCallReceived(t, &calls, convWebhook.Calls())

	goal, found, err := unstructured.NestedString(convertedCowboy.Object, "spec", "goal")
	require.NoError(t, err)
	require.True(t, found, "expected spec.goal to be present on v1alpha2 cowboy after conversion")
	require.Equal(t, "rob the stage coach", goal, "spec.goal should carry the value from spec.intent after v1alpha1→v1alpha2 conversion")
	t.Logf("Conversion webhook calls %d", convWebhook.Calls())

	t.Logf("Creating a new v1alpha2 cowboy new-sheriff in consumer workspace %q", consumerPath)
	v2Cowboy := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "wildwest.dev/v1alpha2",
			"kind":       "Cowboy",
			"metadata": map[string]interface{}{
				"name":      "new-sheriff",
				"namespace": "default",
			},
			"spec": map[string]interface{}{
				"goal": "bring peace to the valley",
			},
		},
	}
	_, err = dynamicClusterClient.
		Cluster(consumerPath).
		Resource(cowboysV2GVR).
		Namespace("default").
		Create(t.Context(), v2Cowboy, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Reading back new-sheriff via v1alpha2 API")
	readBack, err := dynamicClusterClient.
		Cluster(consumerPath).
		Resource(cowboysV2GVR).
		Namespace("default").
		Get(t.Context(), "new-sheriff", metav1.GetOptions{})
	require.NoError(t, err)
	goal, found, err = unstructured.NestedString(readBack.Object, "spec", "goal")
	require.NoError(t, err)
	require.True(t, found, "expected spec.goal on new v1alpha2 cowboy")
	require.Equal(t, "bring peace to the valley", goal)
	t.Logf("Conversion webhook calls %d", convWebhook.Calls())

	t.Logf("Reading new-sheriff via v1alpha1 API – webhook should convert spec.goal→spec.intent")
	var backConverted *unstructured.Unstructured
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		backConverted, err = dynamicClusterClient.
			Cluster(consumerPath).
			Resource(cowboysV1GVR).
			Namespace("default").
			Get(t.Context(), "new-sheriff", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting new-sheriff via v1alpha1: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	intent, found, err := unstructured.NestedString(backConverted.Object, "spec", "intent")
	require.NoError(t, err)
	require.True(t, found, "expected spec.intent on cowboy after v1alpha2→v1alpha1 conversion")
	require.Equal(t, "bring peace to the valley", intent, "spec.intent should carry the value from spec.goal after v1alpha2→v1alpha1 conversion")
	ensureCallReceived(t, &calls, convWebhook.Calls())
}

func ensureCallReceived(t *testing.T, calls *int, newCalls int) {
	t.Helper()
	require.Greater(t, newCalls, *calls, "should have received a new call in webhook")
	*calls = newCalls
}
