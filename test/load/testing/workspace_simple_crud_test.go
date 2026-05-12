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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	"github.com/kcp-dev/kcp/test/load/pkg/framework"
	"github.com/kcp-dev/kcp/test/load/pkg/measurement"
	"github.com/kcp-dev/kcp/test/load/pkg/stats"
	"github.com/kcp-dev/kcp/test/load/pkg/tree"
	"github.com/kcp-dev/kcp/test/load/pkg/tuningset"
)

const crudConfigMapQPS = 150 // equivalent to 600, see TODO comment below

func TestWorkspaceSimpleCRUD(t *testing.T) {
	cfg := framework.Require(t, framework.KCPFrontProxyKubeconfig)

	client, err := kcpclientset.NewForConfig(cfg.FrontProxyKubeconfig)
	require.NoError(t, err)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg.FrontProxyKubeconfig)
	require.NoError(t, err)

	var sections []measurement.Section

	wt := defaultTree()

	// Ensure workspaces exist, creating them if necessary.
	exist, err := workspacesExist(wt, client, workspaceCount)
	require.NoError(t, err)
	if exist {
		t.Logf("workspaces already exist, skipping creation")
	} else {
		t.Logf("Creating required workspaces")
		createSection := createWorkspaces(t, client, createWorkspaceQPS)
		sections = append(sections, createSection)
	}

	t.Logf("Running configmap CRUD operations")
	crudSection := crudConfigMaps(t, wt, kubeClusterClient, crudConfigMapQPS)
	sections = append(sections, crudSection)

	report := NewKCPReport(t, "Workspace Simple Configmap CRUD", cfg.FrontProxyKubeconfig)
	report.Sections = sections
	report.PrettyPrint(os.Stdout)

	for _, sec := range sections {
		require.Empty(t, sec.Errors, "section %q encountered errors", sec.Title)
	}
}

// crudConfigMaps performs a Create/Update/Delete cycle for a ConfigMap in each
// of the workspaces.
func crudConfigMaps(t *testing.T, wt tree.WorkspaceTree, kubeClusterClient kcpkubernetesclientset.ClusterInterface, qps float64) measurement.Section {
	t.Helper()

	section := measurement.Section{
		Title: "ConfigMap CRUD",
		Parameters: []measurement.Parameter{
			{Key: "Workspaces", Value: fmt.Sprintf("%d", workspaceCount)},
			{Key: "QPS", Value: fmt.Sprintf("%f", qps*4)}, // TODO: for now multiply by 4 because we are doing 4 network calls per action, but this needs to be changed inside the framework next to be precise
		},
		Sink: &measurement.Memory{
			Stats: []stats.NamedStat{stats.P99(), stats.Avg()},
		},
	}

	ts := tuningset.NewUniformQPS(qps, workspaceCount, 1)
	section.Start()
	action := func(seq int, s measurement.Sink) error {
		cmClient := kubeClusterClient.Cluster(wt.PathForSequenceNumber(seq)).CoreV1().ConfigMaps("default")

		defer measurement.RecordElapsedDurationMS(time.Now(), s)

		ctx := context.Background()
		cmName := fmt.Sprintf("loadtest-cm-%d", seq)

		// Create
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: "default",
			},
			Data: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		}

		opStart := time.Now()
		created, err := cmClient.Create(ctx, cm, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create configmap: %w", err)
		}
		s.Drop(measurement.Measurement{Name: "create_duration_ms", Value: time.Since(opStart).Seconds() * 1000})

		// Get
		opStart = time.Now()
		_, err = cmClient.Get(ctx, cmName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get configmap: %w", err)
		}
		s.Drop(measurement.Measurement{Name: "get_duration_ms", Value: time.Since(opStart).Seconds() * 1000})

		// Update
		created.Data = map[string]string{
			"key1": "updated-value1",
			"key2": "updated-value2",
			"key3": "new-value3",
		}
		opStart = time.Now()
		_, err = cmClient.Update(ctx, created, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update configmap: %w", err)
		}
		s.Drop(measurement.Measurement{Name: "update_duration_ms", Value: time.Since(opStart).Seconds() * 1000})

		// Delete
		opStart = time.Now()
		err = cmClient.Delete(ctx, cmName, metav1.DeleteOptions{})
		if err != nil {
			return fmt.Errorf("failed to delete configmap: %w", err)
		}
		s.Drop(measurement.Measurement{Name: "delete_duration_ms", Value: time.Since(opStart).Seconds() * 1000})

		return nil
	}

	section.Errors = framework.Execute(ts, action, section.Sink)
	section.End()

	return section
}
