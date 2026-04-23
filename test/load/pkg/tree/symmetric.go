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

var _ WorkspaceTree = (*symmetricTree)(nil)

// symmetricTree is a workspace tree where each node has the same number of
// children and all leaf nodes are at the same depth. If the total count does
// not perfectly fill all levels, the last level is partially filled
// left-to-right.
//
// Workspaces are numbered 1..count in BFS order (level by level, left to
// right). The branching factor is computed from count and depth as the smallest
// integer b such that b + b² + … + b^depth >= count.
type symmetricTree struct {
	root            logicalcluster.Path
	Count           int
	Depth           int
	BranchingFactor int
}

// NewSymmetricTree creates a SymmetricTree with the given total workspace count
// and depth, rooted at the given logical cluster path. The branching factor is
// the smallest integer b such that a full b-ary tree of the given depth has at
// least count nodes. The last level may be partially filled.
func NewSymmetricTree(root logicalcluster.Path, count, depth int) *symmetricTree {
	b := findBranchingFactor(count, depth)
	return &symmetricTree{root: root, Count: count, Depth: depth, BranchingFactor: b}
}

func (t *symmetricTree) WorkspaceName(seq int) string {
	return fmt.Sprintf("%s%d", LoadTestWsNamePrefix, seq)
}

func (t *symmetricTree) Root() logicalcluster.Path {
	return t.root
}

func (t *symmetricTree) PathForSequenceNumber(seq int) logicalcluster.Path {
	if seq == 0 {
		return t.root
	}

	// Build ancestor chain from seq up to root.
	chain := []int{seq}
	for {
		p := t.ParentSequenceNumber(chain[len(chain)-1])
		if p == 0 {
			break
		}
		chain = append(chain, p)
	}

	// Assemble path from root down to seq.
	path := t.root
	for i := len(chain) - 1; i >= 0; i-- {
		path = path.Join(t.WorkspaceName(chain[i]))
	}
	return path
}

// ParentSequenceNumber returns the sequence number of the parent workspace, or 0 if the
// parent is the root workspace.
func (t *symmetricTree) ParentSequenceNumber(seq int) int {
	level := t.levelOf(seq)
	if level == 1 {
		return 0
	}

	b := t.BranchingFactor
	startCurrent, _ := t.LevelRange(level)
	startParent, _ := t.LevelRange(level - 1)

	posInLevel := seq - startCurrent
	parentPos := posInLevel / b
	return startParent + parentPos
}

func (t *symmetricTree) NumLevels() int {
	return t.Depth
}

// LevelRange returns the 1-based start sequence number and the count of
// workspaces at the given level. For the last level this may be less than
// b^level when count does not fill the tree completely.
func (t *symmetricTree) LevelRange(level int) (start, count int) {
	b := t.BranchingFactor
	startSeq := 1
	power := 1
	for range level - 1 {
		power *= b
		startSeq += power
	}
	power *= b
	levelCount := power

	remaining := t.Count - (startSeq - 1)
	if levelCount > remaining {
		levelCount = remaining
	}
	return startSeq, levelCount
}

func (t *symmetricTree) String() string {
	return fmt.Sprintf("SymmetricTree{Count: %d, Depth: %d, BranchingFactor: %d}", t.Count, t.Depth, t.BranchingFactor)
}

// totalWorkspaces returns the total number of nodes in a full b-ary tree of
// the given depth: b + b² + … + b^d.
func totalWorkspaces(b, d int) int {
	total := 0
	power := 1
	for range d {
		power *= b
		total += power
	}
	return total
}

// findBranchingFactor returns the smallest integer b >= 1 such that
// totalWorkspaces(b, depth) >= count.
func findBranchingFactor(count, depth int) int {
	lo, hi := 1, count
	result := count
	for lo <= hi {
		mid := lo + (hi-lo)/2
		if totalWorkspaces(mid, depth) >= count {
			result = mid
			hi = mid - 1
		} else {
			lo = mid + 1
		}
	}
	return result
}

// levelOf returns the 1-based level for the given 1-based sequence number.
func (t *symmetricTree) levelOf(seq int) int {
	cumulative := 0
	power := 1
	for level := 1; level <= t.Depth; level++ {
		power *= t.BranchingFactor
		cumulative += power
		if seq <= cumulative {
			return level
		}
	}
	return t.Depth
}
