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
	"fmt"

	"github.com/kcp-dev/logicalcluster/v3"
)

var _ WorkspaceTree = (*flatTree)(nil)

// flatTree is a workspace tree where all workspaces are direct children of the
// root cluster. There is no nesting — every workspace lives at depth 1.
// We mainly use this for comparison with nested trees.
type flatTree struct {
	root  logicalcluster.Path
	Count int
}

// NewFlatTree creates a flat workspace tree with the given total count rooted
// at the given logical cluster path.
func NewFlatTree(root logicalcluster.Path, count int) *flatTree {
	return &flatTree{root: root, Count: count}
}

func (t *flatTree) WorkspaceName(seq int) string {
	return fmt.Sprintf("%s%d", LoadTestWsNamePrefix, seq)
}

func (t *flatTree) Root() logicalcluster.Path {
	return t.root
}

func (t *flatTree) PathForSequenceNumber(seq int) logicalcluster.Path {
	if seq == 0 {
		return t.root
	}
	return t.root.Join(t.WorkspaceName(seq))
}

// ParentSequenceNumber always returns 0 because every workspace's parent is
// the root cluster.
func (t *flatTree) ParentSequenceNumber(_ int) int {
	return 0
}

// NumLevels always returns 1.
func (t *flatTree) NumLevels() int {
	return 1
}

// LevelRange returns the full range for the single level.
func (t *flatTree) LevelRange(level int) (start, count int) {
	if level != 1 {
		return 0, 0
	}
	return 1, t.Count
}

func (t *flatTree) String() string {
	return fmt.Sprintf("FlatTree{Count: %d}", t.Count)
}
