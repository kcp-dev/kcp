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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1kcp "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	"github.com/kcp-dev/kcp/test/load/pkg/framework"
	"github.com/kcp-dev/kcp/test/load/pkg/measurement"
	"github.com/kcp-dev/kcp/test/load/pkg/stats"
	"github.com/kcp-dev/kcp/test/load/pkg/tree"
	"github.com/kcp-dev/kcp/test/load/pkg/tuningset"
)

const workspaceCount = 10000
const workspaceDepth = 5
const createWorkspaceQPS = 2.0

func TestWorkspaceCreation(t *testing.T) {
	cfg := framework.Require(t, framework.KCPFrontProxyKubeconfig)

	client, err := kcpclientset.NewForConfig(cfg.FrontProxyKubeconfig)
	require.NoError(t, err)

	sections := []measurement.Section{} //nolint:prealloc

	createSection := createWorkspaces(t, client, createWorkspaceQPS)
	sections = append(sections, createSection)

	report := NewKCPReport(t, "Workspace Creation", cfg.FrontProxyKubeconfig)
	report.Sections = sections
	report.PrettyPrint(os.Stdout)

	for _, sec := range sections {
		require.Empty(t, sec.Errors, "section %q encountered errors", sec.Title)
	}
}

// createWorkspaces creates workspaceCount workspaces under the root workspace
// and waits for each to become Ready.
func createWorkspaces(t *testing.T, client kcpclientset.ClusterInterface, qps float64) measurement.Section {
	t.Helper()
	wt := defaultTree()

	section := measurement.Section{
		Title: "Workspace Creation",
		Parameters: []measurement.Parameter{
			{Key: "WorkspaceTree", Value: wt.String()},
			{Key: "QPS", Value: fmt.Sprintf("%f", qps)},
		},
		Sink: &measurement.Memory{
			Stats: []stats.NamedStat{stats.P99(), stats.Avg()},
		},
	}

	ts := tuningset.NewUniformQPS(qps, workspaceCount, 1)
	section.Start()
	action := func(seq int, s measurement.Sink) error {
		defer measurement.RecordElapsedDurationMS(time.Now(), s)

		ctx := context.Background()
		wsName := wt.WorkspaceName(seq)

		ws := &tenancyv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: wsName,
			},
		}

		// create a client scoped to the parent of the workspace to be created
		parent := wt.PathForSequenceNumber(wt.ParentSequenceNumber(seq))
		wsClient := client.Cluster(parent).TenancyV1alpha1().Workspaces()

		_, err := wsClient.Create(ctx, ws, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("create workspace: %w", err)
		}

		// Poll until the workspace reaches the Ready phase.
		err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
			got, err := wsClient.Get(ctx, wsName, metav1.GetOptions{})
			if err != nil {
				// on errors we want to retry
				return false, nil //nolint:nilerr
			}
			return got.Status.Phase == corev1alpha1kcp.LogicalClusterPhaseReady, nil
		})
		if err != nil {
			return fmt.Errorf("workspace did not become ready: %w", err)
		}

		return nil
	}

	section.Errors = framework.Execute(ts, action, section.Sink)
	section.End()

	return section
}

func defaultTree() tree.WorkspaceTree {
	return tree.NewSymmetricTree(core.RootCluster.Path(), workspaceCount, workspaceDepth)
}
