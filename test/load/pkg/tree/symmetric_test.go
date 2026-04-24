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

package tree

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/sdk/apis/core"
)

func TestNewSymmetricTree(t *testing.T) {
	tests := []struct {
		name          string
		count         int
		depth         int
		wantBranching int
	}{
		{name: "flat", count: 5, depth: 1, wantBranching: 5},
		{name: "binary depth 2 exact", count: 6, depth: 2, wantBranching: 2},
		{name: "binary depth 3 exact", count: 14, depth: 3, wantBranching: 2},
		{name: "ternary depth 2 exact", count: 12, depth: 2, wantBranching: 3},
		{name: "partial fill depth 2", count: 5, depth: 2, wantBranching: 2},
		{name: "linear chain", count: 3, depth: 3, wantBranching: 1},
		{name: "single node", count: 1, depth: 1, wantBranching: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree := NewSymmetricTree(core.RootCluster.Path(), tt.count, tt.depth)
			require.Equal(t, tt.wantBranching, tree.BranchingFactor)
			require.Equal(t, tt.count, tree.Count)
			require.Equal(t, tt.depth, tree.Depth)
		})
	}
}

func TestTotalWorkspaces(t *testing.T) {
	require.Equal(t, 5, totalWorkspaces(5, 1))     // 5
	require.Equal(t, 6, totalWorkspaces(2, 2))     // 2 + 4
	require.Equal(t, 14, totalWorkspaces(2, 3))    // 2 + 4 + 8
	require.Equal(t, 12, totalWorkspaces(3, 2))    // 3 + 9
	require.Equal(t, 3, totalWorkspaces(1, 3))     // 1 + 1 + 1
	require.Equal(t, 1110, totalWorkspaces(10, 3)) // 10 + 100 + 1000
}

func TestSymmetricTreeLevelRange(t *testing.T) {
	t.Run("binary depth 2", func(t *testing.T) {
		tree := NewSymmetricTree(core.RootCluster.Path(), 6, 2)

		start, count := tree.LevelRange(1)
		require.Equal(t, 1, start)
		require.Equal(t, 2, count)

		start, count = tree.LevelRange(2)
		require.Equal(t, 3, start)
		require.Equal(t, 4, count)
	})

	t.Run("binary depth 3", func(t *testing.T) {
		tree := NewSymmetricTree(core.RootCluster.Path(), 14, 3)

		start, count := tree.LevelRange(1)
		require.Equal(t, 1, start)
		require.Equal(t, 2, count)

		start, count = tree.LevelRange(2)
		require.Equal(t, 3, start)
		require.Equal(t, 4, count)

		start, count = tree.LevelRange(3)
		require.Equal(t, 7, start)
		require.Equal(t, 8, count)
	})

	t.Run("partial fill", func(t *testing.T) {
		// count=5, depth=2, b=2: level 1 [1,2], level 2 [3,4,5]
		tree := NewSymmetricTree(core.RootCluster.Path(), 5, 2)

		start, count := tree.LevelRange(1)
		require.Equal(t, 1, start)
		require.Equal(t, 2, count)

		start, count = tree.LevelRange(2)
		require.Equal(t, 3, start)
		require.Equal(t, 3, count)
	})

	t.Run("linear chain", func(t *testing.T) {
		tree := NewSymmetricTree(core.RootCluster.Path(), 3, 3)
		require.Equal(t, 1, tree.BranchingFactor)

		for level := 1; level <= 3; level++ {
			start, count := tree.LevelRange(level)
			require.Equal(t, level, start)
			require.Equal(t, 1, count)
		}
	})
}

func TestSymmetricTreeParentSequenceNumber(t *testing.T) {
	t.Run("binary depth 3", func(t *testing.T) {
		tree := NewSymmetricTree(core.RootCluster.Path(), 14, 3)

		// Level 1: parent is root (0)
		require.Equal(t, 0, tree.ParentSequenceNumber(1))
		require.Equal(t, 0, tree.ParentSequenceNumber(2))

		// Level 2: nodes 3-6, two children per parent
		require.Equal(t, 1, tree.ParentSequenceNumber(3))
		require.Equal(t, 1, tree.ParentSequenceNumber(4))
		require.Equal(t, 2, tree.ParentSequenceNumber(5))
		require.Equal(t, 2, tree.ParentSequenceNumber(6))

		// Level 3: nodes 7-14
		require.Equal(t, 3, tree.ParentSequenceNumber(7))
		require.Equal(t, 3, tree.ParentSequenceNumber(8))
		require.Equal(t, 4, tree.ParentSequenceNumber(9))
		require.Equal(t, 4, tree.ParentSequenceNumber(10))
		require.Equal(t, 5, tree.ParentSequenceNumber(11))
		require.Equal(t, 5, tree.ParentSequenceNumber(12))
		require.Equal(t, 6, tree.ParentSequenceNumber(13))
		require.Equal(t, 6, tree.ParentSequenceNumber(14))
	})

	t.Run("linear chain", func(t *testing.T) {
		tree := NewSymmetricTree(core.RootCluster.Path(), 3, 3)

		require.Equal(t, 0, tree.ParentSequenceNumber(1))
		require.Equal(t, 1, tree.ParentSequenceNumber(2))
		require.Equal(t, 2, tree.ParentSequenceNumber(3))
	})
}

func TestSymmetricTreePathForSequenceNumber(t *testing.T) {
	root := core.RootCluster.Path()

	t.Run("root for seq 0", func(t *testing.T) {
		tree := NewSymmetricTree(root, 6, 2)
		require.Equal(t, root, tree.PathForSequenceNumber(0))
	})

	t.Run("flat depth 1", func(t *testing.T) {
		tree := NewSymmetricTree(root, 3, 1)

		require.Equal(t, root.Join("loadtest-ws-1"), tree.PathForSequenceNumber(1))
		require.Equal(t, root.Join("loadtest-ws-2"), tree.PathForSequenceNumber(2))
		require.Equal(t, root.Join("loadtest-ws-3"), tree.PathForSequenceNumber(3))
	})

	t.Run("binary depth 2", func(t *testing.T) {
		tree := NewSymmetricTree(root, 6, 2)

		require.Equal(t, root.Join("loadtest-ws-1"), tree.PathForSequenceNumber(1))
		require.Equal(t, root.Join("loadtest-ws-2"), tree.PathForSequenceNumber(2))
		require.Equal(t, root.Join("loadtest-ws-1").Join("loadtest-ws-3"), tree.PathForSequenceNumber(3))
		require.Equal(t, root.Join("loadtest-ws-1").Join("loadtest-ws-4"), tree.PathForSequenceNumber(4))
		require.Equal(t, root.Join("loadtest-ws-2").Join("loadtest-ws-5"), tree.PathForSequenceNumber(5))
		require.Equal(t, root.Join("loadtest-ws-2").Join("loadtest-ws-6"), tree.PathForSequenceNumber(6))
	})

	t.Run("binary depth 3", func(t *testing.T) {
		tree := NewSymmetricTree(root, 14, 3)

		// Level 3 leaf: ws-14's parent is ws-6, whose parent is ws-2
		want := root.Join("loadtest-ws-2").Join("loadtest-ws-6").Join("loadtest-ws-14")
		require.Equal(t, want, tree.PathForSequenceNumber(14))

		// Level 3 leaf: ws-7's parent is ws-3, whose parent is ws-1
		want = root.Join("loadtest-ws-1").Join("loadtest-ws-3").Join("loadtest-ws-7")
		require.Equal(t, want, tree.PathForSequenceNumber(7))
	})

	t.Run("linear chain", func(t *testing.T) {
		tree := NewSymmetricTree(root, 3, 3)

		require.Equal(t, root.Join("loadtest-ws-1"), tree.PathForSequenceNumber(1))
		require.Equal(t, root.Join("loadtest-ws-1").Join("loadtest-ws-2"), tree.PathForSequenceNumber(2))
		require.Equal(t, root.Join("loadtest-ws-1").Join("loadtest-ws-2").Join("loadtest-ws-3"), tree.PathForSequenceNumber(3))
	})
}

func TestSymmetricTreeLevelOf(t *testing.T) {
	tree := NewSymmetricTree(core.RootCluster.Path(), 14, 3)

	require.Equal(t, 1, tree.levelOf(1))
	require.Equal(t, 1, tree.levelOf(2))
	require.Equal(t, 2, tree.levelOf(3))
	require.Equal(t, 2, tree.levelOf(6))
	require.Equal(t, 3, tree.levelOf(7))
	require.Equal(t, 3, tree.levelOf(14))
}
