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
	"strings"
	"testing"
)

func TestWorkspacesValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		prefix  string
		cfg     WorkspacesConfig
		wantErr string
	}{
		{
			name:   "valid defaults",
			prefix: "perf",
			cfg:    WorkspacesConfig{Depth: 3, Count: 50},
		},
		{
			name:    "depth zero rejected",
			prefix:  "perf",
			cfg:     WorkspacesConfig{Depth: 0, Count: 10},
			wantErr: "--tree-depth must be >= 1",
		},
		{
			name:    "negative count rejected",
			prefix:  "perf",
			cfg:     WorkspacesConfig{Depth: 2, Count: -1},
			wantErr: "--tree-count must be >= 0",
		},
		{
			name:   "count zero allowed",
			prefix: "perf",
			cfg:    WorkspacesConfig{Depth: 2, Count: 0},
		},
		{
			name:    "uppercase prefix rejected",
			prefix:  "Perf",
			cfg:     WorkspacesConfig{Depth: 2, Count: 5},
			wantErr: "invalid workspace name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &workspacesScenario{}
			s.SetWorkspacesConfig(tt.cfg)
			err := s.Validate(tt.prefix)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("Validate() error = %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate() unexpected error: %v", err)
			}
		})
	}
}

func TestWorkspacesGenerateTree(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		depth  int
		count  int
		prefix string
	}{
		{name: "small flat tree", depth: 1, count: 5, prefix: "p"},
		{name: "small deep tree", depth: 3, count: 20, prefix: "p"},
		{name: "single node tree", depth: 5, count: 1, prefix: "p"},
		{name: "empty tree", depth: 2, count: 0, prefix: "p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &workspacesScenario{}
			s.SetWorkspacesConfig(WorkspacesConfig{Depth: tt.depth, Count: tt.count, Seed: 42})

			nodes := s.generateTree(tt.prefix)

			if len(nodes) != tt.count+1 {
				t.Fatalf("len(nodes) = %d, want %d (org + %d children)", len(nodes), tt.count+1, tt.count)
			}

			// First node is the org at depth 0.
			if nodes[0].depth != 0 {
				t.Errorf("nodes[0].depth = %d, want 0", nodes[0].depth)
			}
			if nodes[0].parentIdx != -1 {
				t.Errorf("nodes[0].parentIdx = %d, want -1", nodes[0].parentIdx)
			}
			if nodes[0].name != tt.prefix+OrgSuffix {
				t.Errorf("nodes[0].name = %q, want %q", nodes[0].name, tt.prefix+OrgSuffix)
			}

			seen := map[string]bool{nodes[0].name: true}
			for i := 1; i < len(nodes); i++ {
				n := nodes[i]
				if n.parentIdx < 0 || n.parentIdx >= i {
					t.Errorf("node %d has parentIdx %d, must be in [0,%d)", i, n.parentIdx, i)
				}
				if got, want := n.depth, nodes[n.parentIdx].depth+1; got != want {
					t.Errorf("node %d depth = %d, want %d", i, got, want)
				}
				if n.depth > tt.depth {
					t.Errorf("node %d depth %d exceeds max %d", i, n.depth, tt.depth)
				}
				if n.depth < 1 {
					t.Errorf("node %d depth %d must be >= 1 for child", i, n.depth)
				}
				if seen[n.name] {
					t.Errorf("node %d has duplicate name %q", i, n.name)
				}
				seen[n.name] = true
			}
		})
	}
}

func TestWorkspacesGenerateTreeDeterministic(t *testing.T) {
	t.Parallel()
	cfg := WorkspacesConfig{Depth: 3, Count: 50, Seed: 1234}

	s1 := &workspacesScenario{}
	s1.SetWorkspacesConfig(cfg)
	t1 := s1.generateTree("p")

	s2 := &workspacesScenario{}
	s2.SetWorkspacesConfig(cfg)
	t2 := s2.generateTree("p")

	if len(t1) != len(t2) {
		t.Fatalf("tree lengths differ: %d vs %d", len(t1), len(t2))
	}
	for i := range t1 {
		if t1[i] != t2[i] {
			t.Errorf("trees diverge at node %d: %+v vs %+v", i, t1[i], t2[i])
		}
	}
}

func TestWorkspacesGenerateTreeCached(t *testing.T) {
	t.Parallel()
	s := &workspacesScenario{}
	s.SetWorkspacesConfig(WorkspacesConfig{Depth: 3, Count: 10, Seed: 0})

	first := s.generateTree("p")
	second := s.generateTree("p")

	if len(first) == 0 || len(second) == 0 {
		t.Fatal("generateTree returned an empty slice; expected non-empty cached result")
	}
	if &first[0] != &second[0] {
		t.Error("generateTree returned different slices on repeat call; expected cached result")
	}
}

func TestWorkspacesSteps(t *testing.T) {
	t.Parallel()
	s := &workspacesScenario{}
	s.SetWorkspacesConfig(WorkspacesConfig{Depth: 2, Count: 5, Seed: 1})

	steps := s.Steps("perf")
	if len(steps) != 2 {
		t.Fatalf("Steps() returned %d steps, want 2", len(steps))
	}
	if steps[0].Cleanup == nil {
		t.Error("Steps()[0] (org creation) must have a Cleanup func")
	}
	if steps[1].Cleanup != nil {
		t.Error("Steps()[1] (tree creation) should not have a Cleanup func; org deletion cascades")
	}
	if !strings.Contains(steps[0].Description, "perf-org") {
		t.Errorf("Steps()[0].Description = %q, want it to contain %q", steps[0].Description, "perf-org")
	}
}
