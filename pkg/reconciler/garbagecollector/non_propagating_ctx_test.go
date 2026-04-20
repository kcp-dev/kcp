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

package garbagecollector

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNonPropagatingCtxCancelDoesNotPropagate(t *testing.T) {
	t.Parallel()

	parent, parentCancel := context.WithCancel(t.Context())
	defer parentCancel()

	npc := newNonPropagatingCtx(parent)

	// Derive a child context from the non-propagating context.
	child, childCancel := context.WithCancel(npc)
	defer childCancel()

	npc.cancel()

	require.ErrorIs(t, npc.Err(), context.Canceled, "nonPropagatingCtx should be done after cancel")

	// Give a moment for any (incorrect) propagation goroutine to fire.
	time.Sleep(50 * time.Millisecond)

	assert.NoError(t, child.Err(), "child context should not be cancelled")
}

func TestNonPropagatingCtxParentCancellationPropagates(t *testing.T) {
	t.Parallel()

	parent, parentCancel := context.WithCancel(t.Context())
	npc := newNonPropagatingCtx(parent)

	parentCancel()

	select {
	case <-npc.Done():
	case <-time.After(time.Second):
		t.Fatal("expected nonPropagatingCtx to be done after parent cancel")
	}
}

func TestNonPropagatingCtxValue(t *testing.T) {
	t.Parallel()

	type key struct{}
	parent := context.WithValue(t.Context(), key{}, "hello")
	npc := newNonPropagatingCtx(parent)

	assert.Equal(t, "hello", npc.Value(key{}))
}
