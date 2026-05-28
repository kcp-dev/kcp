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

package scenarios

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/logicalcluster/v3"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

const (
	stateKeyTreeOrgPath = "tree-org-path"
	stateKeyTreeCreated = "tree-created"

	treeProgressInterval = 25
)

// WorkspacesConfig configures the workspaces (performance) scenario.
type WorkspacesConfig struct {
	// Depth is the maximum depth of the generated tree below the org workspace.
	// depth=1 produces a flat fan-out under the org; depth=2 allows grandchildren, etc.
	Depth int
	// Count is the total number of child workspaces to create below the org.
	Count int
	// Seed seeds the PRNG used to shape the tree. Zero means "pick at runtime".
	Seed int64
}

// WorkspacesConfigurable lets the plugin pass scenario-specific options
// without leaking implementation details through the Scenario interface.
type WorkspacesConfigurable interface {
	SetWorkspacesConfig(cfg WorkspacesConfig)
}

type workspacesScenario struct {
	depth int
	count int
	seed  int64

	once sync.Once
	tree []treeNode
}

type treeNode struct {
	name      string
	parentIdx int // -1 means the org workspace
	depth     int // 0 = org, 1..depth = children
}

func (s *workspacesScenario) Name() string { return "workspaces" }

func (s *workspacesScenario) SetWorkspacesConfig(cfg WorkspacesConfig) {
	s.depth = cfg.Depth
	s.count = cfg.Count
	s.seed = cfg.Seed
}

func (s *workspacesScenario) EnterPath(state map[string]string) string {
	return state[stateKeyTreeOrgPath]
}

func (s *workspacesScenario) Validate(prefix string) error {
	name := prefix + OrgSuffix
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return fmt.Errorf("--name-prefix %q produces invalid workspace name %q: %v", prefix, name, errs)
	}
	if s.count > 0 {
		last := childName(prefix, s.count)
		if errs := validation.IsDNS1123Label(last); len(errs) > 0 {
			return fmt.Errorf("--name-prefix %q with --tree-count %d produces invalid workspace name %q: %v", prefix, s.count, last, errs)
		}
	}
	if s.depth < 1 {
		return fmt.Errorf("--tree-depth must be >= 1, got %d", s.depth)
	}
	if s.count < 0 {
		return fmt.Errorf("--tree-count must be >= 0, got %d", s.count)
	}
	return nil
}

func (s *workspacesScenario) Steps(prefix string) []Step {
	orgName := prefix + OrgSuffix

	return []Step{
		{
			Description:        fmt.Sprintf("Creating organization workspace %q", orgName),
			CleanupDescription: fmt.Sprintf("Deleting organization workspace %q (cascades %d child workspaces)", orgName, s.count),
			Execute: func(ctx context.Context, execCtx ExecutionContext) error {
				return createWorkspaceStep(ctx, execCtx, logicalcluster.NewPath("root"), orgName,
					&tenancyv1alpha1.WorkspaceTypeReference{Name: "organization", Path: "root"},
					map[string]string{quickstartLabel: "true", quickstartPrefixLabel: prefix},
					stateKeyTreeOrgPath)
			},
			Cleanup: func(ctx context.Context, execCtx ExecutionContext) error {
				return deleteOrgAndWait(ctx, execCtx, orgName)
			},
		},
		{
			Description: fmt.Sprintf("Creating %d workspaces (max depth %d, seed %d)", s.count, s.depth, s.effectiveSeed()),
			Execute: func(ctx context.Context, execCtx ExecutionContext) error {
				return s.createTree(ctx, execCtx, prefix)
			},
		},
	}
}

func (s *workspacesScenario) Samples(_ string) []Step { return nil }

func (s *workspacesScenario) PrintSummary(out io.Writer, prefix string, state map[string]string) error {
	orgName := prefix + OrgSuffix
	orgPath := state[stateKeyTreeOrgPath]
	created := state[stateKeyTreeCreated]
	if created == "" {
		created = "0"
	}

	_, err := fmt.Fprintf(out, `
Quickstart complete! Here's what was created:

  Workspace hierarchy:
    root
    +-- %s (organization)
        +-- %s child workspaces in a random tree (max depth %d, seed %d)

  Org path: %s

  Explore:
    kubectl ws :%s
    kubectl get workspaces

  Cleanup:
    kubectl kcp quickstart --scenario workspaces --cleanup --name-prefix %s
`,
		orgName,
		created,
		s.depth,
		s.effectiveSeed(),
		orgPath,
		orgPath,
		prefix,
	)
	return err
}

// effectiveSeed returns the seed actually used: either the explicit one, or a
// stable per-instance value derived once from the wall clock.
func (s *workspacesScenario) effectiveSeed() int64 {
	s.once.Do(func() {
		if s.seed == 0 {
			s.seed = time.Now().UnixNano()
		}
	})
	return s.seed
}

// generateTree builds the parent-child structure deterministically given the
// scenario config. It is cached so cleanup and run see the same shape.
func (s *workspacesScenario) generateTree(prefix string) []treeNode {
	if s.tree != nil {
		return s.tree
	}

	rng := rand.New(rand.NewSource(s.effectiveSeed()))

	// index 0 is the org (depth 0). Children come after.
	nodes := []treeNode{{name: prefix + OrgSuffix, parentIdx: -1, depth: 0}}

	for i := 1; i <= s.count; i++ {
		eligible := make([]int, 0, len(nodes))
		for j, n := range nodes {
			if n.depth < s.depth {
				eligible = append(eligible, j)
			}
		}
		parentIdx := eligible[rng.Intn(len(eligible))]
		nodes = append(nodes, treeNode{
			name:      childName(prefix, i),
			parentIdx: parentIdx,
			depth:     nodes[parentIdx].depth + 1,
		})
	}

	s.tree = nodes
	return s.tree
}

func (s *workspacesScenario) createTree(ctx context.Context, execCtx ExecutionContext, prefix string) error {
	nodes := s.generateTree(prefix)
	if len(nodes) <= 1 {
		fmt.Fprintf(execCtx.Out, "  No child workspaces to create (count=0)\n")
		execCtx.State[stateKeyTreeCreated] = "0"
		return nil
	}

	// Paths indexed alongside nodes. nodePaths[0] is the org.
	nodePaths := make([]logicalcluster.Path, len(nodes))
	nodePaths[0] = logicalcluster.NewPath(execCtx.State[stateKeyTreeOrgPath])

	wsType := &tenancyv1alpha1.WorkspaceTypeReference{Name: "universal", Path: "root"}
	labels := map[string]string{quickstartLabel: "true", quickstartPrefixLabel: prefix}

	start := time.Now()
	created := 0

	for i := 1; i < len(nodes); i++ {
		n := nodes[i]
		parentPath := nodePaths[n.parentIdx]

		childPath, _, err := createWorkspaceAndWait(ctx, execCtx.KCPClusterClient, parentPath, n.name, wsType, labels)
		if err != nil {
			return fmt.Errorf("creating workspace %d/%d (%q at depth %d under %s): %w",
				i, len(nodes)-1, n.name, n.depth, parentPath, err)
		}
		nodePaths[i] = childPath
		created++

		if created%treeProgressInterval == 0 || i == len(nodes)-1 {
			elapsed := time.Since(start)
			rate := float64(created) / elapsed.Seconds()
			fmt.Fprintf(execCtx.Out, "  Progress: %d/%d workspaces created (%.1f ws/s, depth so far: %d)\n",
				created, len(nodes)-1, rate, n.depth)
		}
	}

	execCtx.State[stateKeyTreeCreated] = fmt.Sprintf("%d", created)
	return nil
}

func childName(prefix string, i int) string {
	return fmt.Sprintf("%s-ws-%d", prefix, i)
}

// deleteOrgAndWait removes the org workspace and waits for the cascading
// deletion of all children to complete. Shared shape with api-provider's
// org cleanup but kept local to avoid coupling the two scenarios.
func deleteOrgAndWait(ctx context.Context, execCtx ExecutionContext, orgName string) error {
	rootPath := logicalcluster.NewPath("root")
	err := execCtx.KCPClusterClient.Cluster(rootPath).TenancyV1alpha1().Workspaces().
		Delete(ctx, orgName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	fmt.Fprintf(execCtx.Out, "  Waiting for workspace %q and all children to finish terminating...\n", orgName)
	lastLog := time.Now()
	return wait.PollUntilContextCancel(ctx, pollIntervalCleanup, true,
		func(ctx context.Context) (bool, error) {
			ws, err := execCtx.KCPClusterClient.Cluster(rootPath).TenancyV1alpha1().Workspaces().
				Get(ctx, orgName, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			if err != nil {
				return false, err
			}

			if time.Since(lastLog) >= logThrottleInterval {
				if ws.DeletionTimestamp != nil {
					fmt.Fprintf(execCtx.Out, "  Still terminating (cascading deletion in progress)...\n")
				} else {
					fmt.Fprintf(execCtx.Out, "  Deletion pending (phase: %s)...\n", ws.Status.Phase)
				}
				lastLog = time.Now()
			}

			return false, nil
		},
	)
}
