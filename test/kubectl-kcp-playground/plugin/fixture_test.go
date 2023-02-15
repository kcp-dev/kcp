/*
Copyright 2023 The KCP Authors.

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

package plugin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// NOTE: this test showcases how in future we can eventually use the PlaygroundFixture in actual E2E tests,
// thus providing a simple way to setup the initial scenario for the test case.
// But this is something we can consider when the functional coverage of the playground will be higher.

func TestFixture(t *testing.T) {
	// comment to run the test.
	t.Skip()

	spec := &PlaygroundSpec{
		Shards: []ShardSpec{
			{
				Name:       MainShardName,
				Workspaces: nil,
			},
		},
		PClusters: nil,
	}

	playground := NewPlaygroundFixture(t, spec).Start(t)

	require.Equal(t, len(spec.Shards), len(playground.Shards), "failed to get the desired number of running shards")
}
