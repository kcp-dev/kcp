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

package contextmanager

import (
	"context"
	"fmt"
	"sync"
)

// Manager tracks contexts derived from the root context.
type Manager[K comparable] struct {
	root       context.Context //nolint:containedctx
	cancelRoot context.CancelCauseFunc
	entries    sync.Map // K → *entry
}

type entry struct {
	ctx    context.Context //nolint:containedctx
	cancel context.CancelCauseFunc
}

// New creates a new context manager.
func New[K comparable](root context.Context) *Manager[K] {
	ctx, cancel := context.WithCancelCause(root)
	return &Manager[K]{root: ctx, cancelRoot: cancel}
}

// ContextFor returns a new context that is derived from parent.
// The context will be cancelled if either the manager's root context or the respective key context is cancelled.
func (m *Manager[K]) ContextFor(parent context.Context, key K) (context.Context, context.CancelFunc) {
	keyCtx := m.getContext(key)

	ctx, cancel := context.WithCancelCause(parent)
	stop := context.AfterFunc(keyCtx, func() {
		cancel(fmt.Errorf("%v cancelled", key))
	})

	cleanup := func() {
		stop()
		cancel(nil)
	}

	return ctx, cleanup
}

func (m *Manager[K]) getContext(key K) context.Context {
	// Fast path - a context exists for the key
	if stored, loaded := m.entries.Load(key); loaded {
		return stored.(*entry).ctx
	}

	// Slow path - a context does not exist
	ctx, cancel := context.WithCancelCause(m.root)
	e := &entry{ctx: ctx, cancel: cancel}

	stored, loaded := m.entries.LoadOrStore(key, e)
	if loaded {
		// If loaded is true a value was already stored, cancel the
		// intermitteent context and return the stored value
		cancel(nil)
	}
	return stored.(*entry).ctx
}

func (m *Manager[K]) Has(key K) bool {
	_, ok := m.entries.Load(key)
	return ok
}

// Cancel cancels the context for the given key.
func (m *Manager[K]) Cancel(key K) {
	v, loaded := m.entries.LoadAndDelete(key)
	if !loaded {
		return
	}
	v.(*entry).cancel(fmt.Errorf("%v cancelled", key))
}

// CancelAll cancels the root context, which propagates to all contexts created by .ContextFor.
func (m *Manager[K]) CancelAll() {
	m.cancelRoot(fmt.Errorf("context manager shut down"))
}
