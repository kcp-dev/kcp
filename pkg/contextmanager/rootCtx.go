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
	"sync"

	"golang.org/x/sync/singleflight"
)

// rootCtx abstracts the synchronisation of getting the shared keyed contexts.
type rootCtx struct {
	root       context.Context //nolint:containedctx
	rootCancel context.CancelCauseFunc
	// entries is the hot path quick lookup
	entries sync.Map
	// group is a singleflight group to synchronize slow path context
	// creation
	group singleflight.Group
	// The mutex is to synchronize the slow path and context
	// cancellation
	lock sync.Mutex
}

type entry struct {
	ctx    context.Context //nolint:containedctx
	cancel context.CancelCauseFunc
}

func newRootCtx(ctx context.Context) *rootCtx {
	rc := new(rootCtx)
	rc.root, rc.rootCancel = context.WithCancelCause(ctx)
	return rc
}

func (rc *rootCtx) context(key string) (context.Context, context.CancelCauseFunc) {
	stored, loaded := rc.entries.Load(key)
	if loaded {
		return stored.(*entry).ctx, stored.(*entry).cancel
	}

	// ignoring the error as the .Do only returns the error from the
	// passed function
	built, _, _ := rc.group.Do(key, func() (any, error) {
		// locking to synchronize with the cancelFor
		rc.lock.Lock()
		defer rc.lock.Unlock()

		// Double check the stored entries
		stored, loaded := rc.entries.Load(key)
		if loaded {
			return stored, nil
		}

		// Create and store the entry
		ctx, cancel := context.WithCancelCause(rc.root)
		e := &entry{ctx: ctx, cancel: cancel}
		rc.entries.Store(key, e)
		return e, nil
	})

	return built.(*entry).ctx, built.(*entry).cancel
}

func (rc *rootCtx) cancel(key string, reason error) {
	rc.lock.Lock()
	defer rc.lock.Unlock()

	if stored, loaded := rc.entries.Load(key); loaded {
		stored.(*entry).cancel(reason)
		return
	}

	// store a pre-cancelled context so following calls don't get
	// a fresh context.
	ctx, cancel := context.WithCancelCause(rc.root)
	cancel(reason)
	e := &entry{ctx: ctx, cancel: cancel}
	rc.entries.Store(key, e)
}

func (rc *rootCtx) delete(key string, reason error) {
	rc.lock.Lock()
	defer rc.lock.Unlock()

	stored, loaded := rc.entries.LoadAndDelete(key)
	if !loaded {
		return
	}
	stored.(*entry).cancel(reason)
}

func (rc *rootCtx) cancelAll(reason error) {
	rc.rootCancel(reason)
}
