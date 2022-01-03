/*
Copyright 2022 The KCP Authors.

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

package apiserver

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/storage/etcd3"
)

func TestShardedChunkedStates_UpdateWith(t *testing.T) {
	encodeContinue := func(key string, resourceVersion int64, t *testing.T) string {
		encoded, err := etcd3.EncodeContinue(sillyPrefix+key, sillyPrefix, resourceVersion)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}

	// start with no existing state
	var state *ShardedChunkedStates
	var parseErr error
	state, parseErr = NewChunkedState("", []string{"first", "second", "third"}, 1)
	if parseErr != nil {
		t.Fatal(parseErr)
	}

	for _, step := range []struct {
		name     string
		update   metav1.ListInterface
		expected *ShardedChunkedStates
		done     bool
	}{
		{
			name: "first shard gives us all the data it has",
			update: &metav1.ListMeta{
				ResourceVersion: "123",
				Continue:        "",
			},
			expected: &ShardedChunkedStates{
				ShardResourceVersion: 1,
				ResourceVersions: []ShardedChunkedState{
					{Identifier: "first", ResourceVersion: 123},
					{Identifier: "second"},
					{Identifier: "third"},
				},
			},
		},
		{
			name: "second shard gives us partial data",
			update: &metav1.ListMeta{
				ResourceVersion: "3452345",
				Continue:        encodeContinue("whatever", 3452345, t),
			},
			expected: &ShardedChunkedStates{
				ShardResourceVersion: 1,
				ResourceVersions: []ShardedChunkedState{
					{Identifier: "first", ResourceVersion: 123},
					{Identifier: "second", ResourceVersion: 3452345, StartKey: "whatever"},
					{Identifier: "third"},
				},
			},
		},
		{
			name: "second shard gives us the rest of its data",
			update: &metav1.ListMeta{
				ResourceVersion: "3452345",
				Continue:        "",
			},
			expected: &ShardedChunkedStates{
				ShardResourceVersion: 1,
				ResourceVersions: []ShardedChunkedState{
					{Identifier: "first", ResourceVersion: 123},
					{Identifier: "second", ResourceVersion: 3452345},
					{Identifier: "third"},
				},
			},
		},
		{
			name: "third shard gives us all of its data",
			update: &metav1.ListMeta{
				ResourceVersion: "55",
				Continue:        "",
			},
			expected: &ShardedChunkedStates{
				ShardResourceVersion: 1,
				ResourceVersions: []ShardedChunkedState{
					{Identifier: "first", ResourceVersion: 123},
					{Identifier: "second", ResourceVersion: 3452345},
					{Identifier: "third", ResourceVersion: 55},
				},
			},
			done: true,
		},
	} {
		identifier, _, err := state.NextQuery()
		if err != nil {
			t.Fatalf("%s: could not get next query: %v", step.name, err)
		}
		// update with a successful request
		if err := state.UpdateWith(identifier, step.update); err != nil {
			t.Fatalf("%s: could not update: %v", step.name, err)
		}
		// ensure we have the correct state
		if diff := cmp.Diff(state, step.expected); diff != "" {
			t.Fatalf("%s: got incorrect state after update: %v", step.name, diff)
		}
		// pretend we're sending this to the client and getting it back on the next request
		encoded, err := state.Encode()
		if err != nil {
			t.Fatalf("%s: could not encode state to continue: %v", step.name, err)
		}
		if step.done {
			if encoded != "" {
				t.Fatalf("%s: expected to be done with no continue token, but got: %v", step.name, encoded)
			}
			break
		}
		// update our state before sending off the next request
		state, parseErr = NewChunkedState(encoded, nil, 0)
		if parseErr != nil {
			t.Fatalf("%s: could not parse encoded state: %v", step.name, parseErr)
		}
		if diff := cmp.Diff(state, step.expected); diff != "" {
			t.Fatalf("%s: got incorrect state from parsing: %v", step.name, diff)
		}
	}
}
