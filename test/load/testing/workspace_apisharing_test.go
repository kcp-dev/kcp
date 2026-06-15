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

package testing

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	"github.com/kcp-dev/kcp/test/load/pkg/framework"
	"github.com/kcp-dev/kcp/test/load/pkg/measurement"
	"github.com/kcp-dev/kcp/test/load/pkg/stats"
	"github.com/kcp-dev/kcp/test/load/pkg/tree"
	"github.com/kcp-dev/kcp/test/load/pkg/tuningset"
)

//nolint:paralleltest // load test against shared kcp cluster, parallel execution would conflict on workspace names and skew timing measurements
func TestAPISharing(t *testing.T) {
	cfg := framework.Require(t, framework.KCPFrontProxyKubeconfig)
	params := testConfig.Params

	client, err := kcpclientset.NewForConfig(cfg.FrontProxyKubeconfig)
	require.NoError(t, err)

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg.FrontProxyKubeconfig)
	require.NoError(t, err)

	var sections []measurement.Section

	wt := params.WorkspaceTree()

	// Ensure workspaces exist, creating them if necessary.
	exist, err := workspacesExist(t.Context(), wt, client, params.WorkspaceCount)
	require.NoError(t, err)
	if exist {
		t.Logf("workspaces already exist, skipping creation")
	} else {
		t.Logf("Creating required workspaces")
		createSection := createWorkspaces(t, client, wt, params.CreateWorkspaceQPS, params.WorkspaceCount)
		sections = append(sections, createSection)
	}

	t.Logf("Creating APIExports in provider workspaces")
	exportSection := createAPIExports(t, client, wt, params.CreateAPIExportQPS, params.ProviderWorkspacesCount)
	sections = append(sections, exportSection)

	t.Logf("Creating APIBindings in consumer workspaces")
	bindingSection := createAPIBindings(t, client, wt, params.CreateAPIBindingQPS, params.ProviderWorkspacesCount, params.ConsumerWorkspacesCount, params.BindingsPerConsumer)
	sections = append(sections, bindingSection)

	t.Logf("Running custom resource CRUD operations")
	crudSection := crudCustomResources(t, dynamicClusterClient, wt, params.CRUDSharedAPIQPS, params.ProviderWorkspacesCount, params.ConsumerWorkspacesCount, params.BindingsPerConsumer)
	sections = append(sections, crudSection)

	report := NewKCPReport(t.Context(), t, "API Sharing", cfg.FrontProxyKubeconfig)
	report.Sections = sections
	report.PrettyPrint(os.Stdout)

	for _, sec := range sections {
		require.Empty(t, sec.Errors, "section %q encountered errors", sec.Title)
	}
}

// createAPIExports creates an APIResourceSchema and APIExport in each of the
// first providerCount workspaces in the tree.
func createAPIExports(t *testing.T, client kcpclientset.ClusterInterface, wt tree.WorkspaceTree, qps float64, providerCount int) measurement.Section {
	t.Helper()

	section := measurement.Section{
		Title: "APIExport Creation",
		Parameters: []measurement.Parameter{
			{Key: "ProviderWorkspaces", Value: fmt.Sprintf("%d", providerCount)},
			{Key: "QPS", Value: fmt.Sprintf("%f", qps)},
		},
		Sink: &measurement.Memory{
			Stats: []stats.NamedStat{stats.P99(), stats.Avg()},
		},
	}

	ts := tuningset.NewUniformQPS(qps, providerCount, 1)
	section.Start()
	action := func(ctx context.Context, seq int, s measurement.Sink) error {
		defer measurement.RecordElapsedDurationMS(time.Now(), s)

		wsPath := wt.PathForSequenceNumber(seq)

		schemaName := fmt.Sprintf("v1alpha1.loadtestresources%d.loadtest.kcp.io", seq)
		resourceName := fmt.Sprintf("loadtestresources%d", seq)
		exportName := fmt.Sprintf("loadtest-export-%d", seq)

		// Create the APIResourceSchema.
		schema := &apisv1alpha1.APIResourceSchema{
			ObjectMeta: metav1.ObjectMeta{
				Name: schemaName,
			},
			Spec: apisv1alpha1.APIResourceSchemaSpec{
				Group: "loadtest.kcp.io",
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Kind:     fmt.Sprintf("LoadTestResource%d", seq),
					ListKind: fmt.Sprintf("LoadTestResource%dList", seq),
					Plural:   resourceName,
					Singular: fmt.Sprintf("loadtestresource%d", seq),
				},
				Scope: "Namespaced",
				Versions: []apisv1alpha1.APIResourceVersion{
					{
						Name:    "v1alpha1",
						Served:  true,
						Storage: true,
						Schema: runtime.RawExtension{
							Raw: []byte(`{"type":"object","properties":{"apiVersion":{"type":"string"},"kind":{"type":"string"},"metadata":{"type":"object"},"spec":{"type":"object","properties":{"data":{"type":"string"}}}}}`),
						},
					},
				},
			},
		}

		opStart := time.Now()
		_, err := client.Cluster(wsPath).ApisV1alpha1().APIResourceSchemas().Create(ctx, schema, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create APIResourceSchema in %s: %w", wsPath, err)
		}
		s.Drop(measurement.Measurement{Name: "schema_create_duration_ms", Value: time.Since(opStart).Seconds() * 1000})

		// Create the APIExport referencing the schema.
		export := &apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{
				Name: exportName,
			},
			Spec: apisv1alpha2.APIExportSpec{
				Resources: []apisv1alpha2.ResourceSchema{
					{
						Name:   resourceName,
						Group:  "loadtest.kcp.io",
						Schema: schemaName,
						Storage: apisv1alpha2.ResourceSchemaStorage{
							CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
						},
					},
				},
			},
		}

		opStart = time.Now()
		_, err = client.Cluster(wsPath).ApisV1alpha2().APIExports().Create(ctx, export, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create APIExport in %s: %w", wsPath, err)
		}
		s.Drop(measurement.Measurement{Name: "export_create_duration_ms", Value: time.Since(opStart).Seconds() * 1000})

		// Wait for the APIExport identity to become valid.
		opStart = time.Now()
		err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			got, err := client.Cluster(wsPath).ApisV1alpha2().APIExports().Get(ctx, exportName, metav1.GetOptions{})
			if err != nil {
				return false, nil //nolint:nilerr
			}
			return conditions.IsTrue(got, apisv1alpha2.APIExportIdentityValid), nil
		})
		if err != nil {
			return fmt.Errorf("APIExport in %s did not become ready: %w", wsPath, err)
		}
		s.Drop(measurement.Measurement{Name: "export_ready_duration_ms", Value: time.Since(opStart).Seconds() * 1000})

		return nil
	}

	section.Errors = framework.Execute(t.Context(), ts, action, section.Sink)
	section.End()

	return section
}

// createAPIBindings creates APIBindings in each consumer workspace (sequence
// numbers providerCount+1 through providerCount+consumerCount). Each consumer
// gets bindingsPerConsumer bindings. Providers are assigned via a global
// round-robin across all binding slots (consumerCount * bindingsPerConsumer).
func createAPIBindings(t *testing.T, client kcpclientset.ClusterInterface, wt tree.WorkspaceTree, qps float64, providerCount, consumerCount, bindingsPerConsumer int) measurement.Section {
	t.Helper()

	totalBindings := consumerCount * bindingsPerConsumer

	section := measurement.Section{
		Title: "APIBinding Creation",
		Parameters: []measurement.Parameter{
			{Key: "ConsumerWorkspaces", Value: fmt.Sprintf("%d", consumerCount)},
			{Key: "ProviderWorkspaces", Value: fmt.Sprintf("%d", providerCount)},
			{Key: "BindingsPerConsumer", Value: fmt.Sprintf("%d", bindingsPerConsumer)},
			{Key: "TotalBindings", Value: fmt.Sprintf("%d", totalBindings)},
			{Key: "QPS", Value: fmt.Sprintf("%f", qps)},
		},
		Sink: &measurement.Memory{
			Stats: []stats.NamedStat{stats.P99(), stats.Avg()},
		},
	}

	// Iterate over all binding slots, starting sequence at 0.
	ts := tuningset.NewUniformQPS(qps, totalBindings, 0)
	section.Start()
	action := func(ctx context.Context, seq int, s measurement.Sink) error {
		defer measurement.RecordElapsedDurationMS(time.Now(), s)

		consumerSeq, providerSeq := bindingSlot(seq, providerCount, bindingsPerConsumer)
		consumerPath := wt.PathForSequenceNumber(consumerSeq)
		providerPath := wt.PathForSequenceNumber(providerSeq)
		exportName := fmt.Sprintf("loadtest-export-%d", providerSeq)
		bindingName := fmt.Sprintf("loadtest-binding-%d", providerSeq)

		binding := &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: bindingName,
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

		opStart := time.Now()
		_, err := client.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create APIBinding in %s: %w", consumerPath, err)
		}
		s.Drop(measurement.Measurement{Name: "binding_create_duration_ms", Value: time.Since(opStart).Seconds() * 1000})

		// Wait for the binding to become bound.
		opStart = time.Now()
		err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			got, err := client.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, bindingName, metav1.GetOptions{})
			if err != nil {
				return false, nil //nolint:nilerr
			}
			return got.Status.Phase == apisv1alpha2.APIBindingPhaseBound, nil
		})
		if err != nil {
			return fmt.Errorf("APIBinding in %s did not become bound: %w", consumerPath, err)
		}
		s.Drop(measurement.Measurement{Name: "binding_ready_duration_ms", Value: time.Since(opStart).Seconds() * 1000})

		return nil
	}

	section.Errors = framework.Execute(t.Context(), ts, action, section.Sink)
	section.End()

	return section
}

// crudCustomResources performs a Create/Get/Update/Delete cycle for a custom
// resource in each consumer workspace for each of its bindings. The resource
// type is determined by the provider assignment (same global round-robin as
// createAPIBindings).
func crudCustomResources(t *testing.T, dynamicClient kcpdynamic.ClusterInterface, wt tree.WorkspaceTree, qps float64, providerCount, consumerCount, bindingsPerConsumer int) measurement.Section {
	t.Helper()

	totalBindings := consumerCount * bindingsPerConsumer

	section := measurement.Section{
		Title: "Custom Resource CRUD",
		Parameters: []measurement.Parameter{
			{Key: "ConsumerWorkspaces", Value: fmt.Sprintf("%d", consumerCount)},
			{Key: "BindingsPerConsumer", Value: fmt.Sprintf("%d", bindingsPerConsumer)},
			{Key: "TotalCRUDOps", Value: fmt.Sprintf("%d", totalBindings)},
			{Key: "QPS", Value: fmt.Sprintf("%f", qps*4)},
		},
		Sink: &measurement.Memory{
			Stats: []stats.NamedStat{stats.P99(), stats.Avg()},
		},
	}

	ts := tuningset.NewUniformQPS(qps, totalBindings, 0)
	section.Start()
	action := func(ctx context.Context, seq int, s measurement.Sink) error {
		defer measurement.RecordElapsedDurationMS(time.Now(), s)

		consumerSeq, providerSeq := bindingSlot(seq, providerCount, bindingsPerConsumer)
		consumerPath := wt.PathForSequenceNumber(consumerSeq)
		resourceName := fmt.Sprintf("loadtestresources%d", providerSeq)

		gvr := schema.GroupVersionResource{
			Group:    "loadtest.kcp.io",
			Version:  "v1alpha1",
			Resource: resourceName,
		}

		resClient := dynamicClient.Cluster(consumerPath).Resource(gvr).Namespace("default")
		objName := fmt.Sprintf("loadtest-cr-%d-%d", consumerSeq, providerSeq)

		// Create
		obj := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "loadtest.kcp.io/v1alpha1",
				"kind":       fmt.Sprintf("LoadTestResource%d", providerSeq),
				"metadata": map[string]interface{}{
					"name":      objName,
					"namespace": "default",
				},
				"spec": map[string]interface{}{
					"data": "initial-value",
				},
			},
		}

		opStart := time.Now()
		created, err := resClient.Create(ctx, obj, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create custom resource in %s: %w", consumerPath, err)
		}
		s.Drop(measurement.Measurement{Name: "create_duration_ms", Value: time.Since(opStart).Seconds() * 1000})

		// Get
		opStart = time.Now()
		_, err = resClient.Get(ctx, objName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get custom resource in %s: %w", consumerPath, err)
		}
		s.Drop(measurement.Measurement{Name: "get_duration_ms", Value: time.Since(opStart).Seconds() * 1000})

		// Update
		if err := unstructured.SetNestedField(created.Object, "updated-value", "spec", "data"); err != nil {
			return fmt.Errorf("failed to set spec.data: %w", err)
		}
		opStart = time.Now()
		_, err = resClient.Update(ctx, created, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update custom resource in %s: %w", consumerPath, err)
		}
		s.Drop(measurement.Measurement{Name: "update_duration_ms", Value: time.Since(opStart).Seconds() * 1000})

		// Delete
		opStart = time.Now()
		err = resClient.Delete(ctx, objName, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("failed to delete custom resource in %s: %w", consumerPath, err)
		}
		s.Drop(measurement.Measurement{Name: "delete_duration_ms", Value: time.Since(opStart).Seconds() * 1000})

		return nil
	}

	section.Errors = framework.Execute(t.Context(), ts, action, section.Sink)
	section.End()

	return section
}

// bindingSlot maps a global slot index to the consumer sequence number and
// provider sequence number. Providers are distributed via round-robin across
// all slots (consumerCount * bindingsPerConsumer) so that each provider gets
// an approximately equal share of bindings.
func bindingSlot(globalSeq, providerCount, bindingsPerConsumer int) (consumerSeq, providerSeq int) {
	// +1 because sequence numbers are 1-based: providers occupy 1..providerCount,
	consumerSeq = providerCount + 1 + (globalSeq / bindingsPerConsumer)
	providerSeq = (globalSeq % providerCount) + 1
	return consumerSeq, providerSeq
}
