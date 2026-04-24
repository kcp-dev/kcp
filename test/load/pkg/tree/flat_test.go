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

func TestFlatTreeWorkspaceName(t *testing.T) {
	ft := NewFlatTree(core.RootCluster.Path(), 5)
	require.Equal(t, "loadtest-ws-1", ft.WorkspaceName(1))
	require.Equal(t, "loadtest-ws-5", ft.WorkspaceName(5))
}

func TestFlatTreePathForSequenceNumber(t *testing.T) {
	ft := NewFlatTree(core.RootCluster.Path(), 3)
	root := core.RootCluster.Path()

	require.Equal(t, root, ft.PathForSequenceNumber(0))
	require.Equal(t, root.Join("loadtest-ws-1"), ft.PathForSequenceNumber(1))
	require.Equal(t, root.Join("loadtest-ws-2"), ft.PathForSequenceNumber(2))
	require.Equal(t, root.Join("loadtest-ws-3"), ft.PathForSequenceNumber(3))
}

func TestFlatTreeParentSequenceNumber(t *testing.T) {
	ft := NewFlatTree(core.RootCluster.Path(), 5)
	require.Equal(t, 0, ft.ParentSequenceNumber(1))
	require.Equal(t, 0, ft.ParentSequenceNumber(3))
	require.Equal(t, 0, ft.ParentSequenceNumber(5))
}

func TestFlatTreeNumLevels(t *testing.T) {
	ft := NewFlatTree(core.RootCluster.Path(), 10)
	require.Equal(t, 1, ft.NumLevels())
}

func TestFlatTreeLevelRange(t *testing.T) {
	ft := NewFlatTree(core.RootCluster.Path(), 10)

	start, count := ft.LevelRange(1)
	require.Equal(t, 1, start)
	require.Equal(t, 10, count)

	start, count = ft.LevelRange(2)
	require.Equal(t, 0, start)
	require.Equal(t, 0, count)
}
