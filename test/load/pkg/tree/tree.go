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
	"github.com/kcp-dev/logicalcluster/v3"
)

// LoadTestWsNamePrefix is the common prefix for all load-test workspace names.
const LoadTestWsNamePrefix = "loadtest-ws-"

type WorkspaceTree interface {
	// Root returns the logical cluster path used as the root of this tree.
	Root() logicalcluster.Path

	// WorkspaceName returns the predictable name for a workspace at the given
	// sequence number.
	WorkspaceName(seq int) string

	// PathForSequenceNumber returns the full logical cluster path for the
	// workspace with the given 1-based sequence number.
	PathForSequenceNumber(seq int) logicalcluster.Path

	// ParentSequenceNumber returns the sequence number of the parent
	// workspace, or 0 if the parent is the root workspace.
	ParentSequenceNumber(seq int) int

	// NumLevels returns the number of levels (depth) in the tree.
	NumLevels() int

	// LevelRange returns the 1-based start sequence number and the count of
	// workspaces for the given level (1-indexed).
	LevelRange(level int) (start, count int)

	// String returns a human-readable summary of the tree configuration.
	String() string
}
