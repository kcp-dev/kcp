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
	"errors"
	"fmt"
)

var errShutdown = errors.New("context manager shut down")

// Manager tracks contexts derived from the root context.
type Manager[K fmt.Stringer] struct {
	rc *rootCtx
}

// New creates a new context manager.
func New[K fmt.Stringer](root context.Context) *Manager[K] {
	return &Manager[K]{rc: newRootCtx(root)}
}

// Context returns a new context that is derived from parent.
// The context will be cancelled if either the manager's root context or the respective key context is cancelled.
func (m *Manager[K]) Context(parent context.Context, key K) (context.Context, context.CancelFunc) {
	keyCtx, _ := m.rc.context(key.String())

	ctx, cancel := context.WithCancelCause(parent)
	stop := context.AfterFunc(keyCtx, func() {
		cancel(context.Cause(keyCtx))
	})

	cleanup := func() {
		stop()
		cancel(nil)
	}

	return ctx, cleanup
}

// Cancel cancels the context for the given key with reason.
// If no context exists for the key a context will be created and cancelled.
func (m *Manager[K]) Cancel(key K, reason error) {
	m.rc.cancel(key.String(), reason)
}

// Delete removes the entry for the given key, cancelling its context with reason.
func (m *Manager[K]) Delete(key K, reason error) {
	m.rc.delete(key.String(), reason)
}

// Shutdown cancels the root context, which propagates to all contexts.
func (m *Manager[K]) Shutdown() {
	m.rc.cancelAll(errShutdown)
}
