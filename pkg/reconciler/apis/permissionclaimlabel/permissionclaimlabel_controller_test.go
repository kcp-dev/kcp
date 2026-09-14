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

package permissionclaimlabel

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestWaitForInformerSynced(t *testing.T) {
	t.Parallel()

	gvr := schema.GroupVersionResource{Group: "wild.wild.west", Version: "v1", Resource: "sheriffs"}
	other := schema.GroupVersionResource{Group: "wildwest.dev", Version: "v1alpha1", Resource: "cowboys"}

	type state struct {
		synced    map[schema.GroupVersionResource]struct{}
		notSynced []schema.GroupVersionResource
	}
	syncedState := state{synced: map[schema.GroupVersionResource]struct{}{gvr: {}, other: {}}}
	startingState := state{synced: map[schema.GroupVersionResource]struct{}{other: {}}, notSynced: []schema.GroupVersionResource{gvr}}
	removedState := state{synced: map[schema.GroupVersionResource]struct{}{other: {}}}

	tests := map[string]struct {
		// states is what successive informers() calls return; the last one repeats.
		states  []state
		timeout time.Duration
		want    bool
	}{
		"already synced": {
			states: []state{syncedState},
			want:   true,
		},
		"syncs after a while": {
			states: []state{startingState, startingState, syncedState},
			want:   true,
		},
		"removed before syncing": {
			states: []state{startingState, removedState},
			want:   false,
		},
		"unknown gvr": {
			states: []state{removedState},
			want:   false,
		},
		"never syncs": {
			states:  []state{startingState},
			timeout: 300 * time.Millisecond,
			want:    false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}

			var calls atomic.Int32
			informers := func() (map[schema.GroupVersionResource]struct{}, []schema.GroupVersionResource) {
				i := min(int(calls.Add(1))-1, len(tc.states)-1)
				return tc.states[i].synced, tc.states[i].notSynced
			}

			require.Equal(t, tc.want, waitForInformerSynced(ctx, gvr, informers))
		})
	}
}
