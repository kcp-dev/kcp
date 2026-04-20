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
	"sync"
	"time"
)

// nonPropagatingCtx implements context.Context but doesn't propagate
// cancellation to contexts that were created from it.
type nonPropagatingCtx struct {
	parent context.Context //nolint:containedctx
	done   chan struct{}
	once   sync.Once
}

func newNonPropagatingCtx(parent context.Context) *nonPropagatingCtx {
	c := &nonPropagatingCtx{
		parent: parent,
		done:   make(chan struct{}),
	}
	if parent.Done() != nil {
		go func() {
			select {
			case <-parent.Done():
				c.cancel()
			case <-c.done:
			}
		}()
	}
	return c
}

func (c *nonPropagatingCtx) Deadline() (time.Time, bool) {
	return c.parent.Deadline()
}

func (c *nonPropagatingCtx) Done() <-chan struct{} {
	return c.done
}

func (c *nonPropagatingCtx) Value(key any) any {
	return c.parent.Value(key)
}

func (c *nonPropagatingCtx) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return c.parent.Err()
	}
}

// context.propagateCancel uses the unexported afterFuncer interface.
// When a context is derived from a parent and that parent implements
// this interface it is used to hook the cancellation of the child.
// Returning a noop preents the propagation of the parent cancellation.
func (c *nonPropagatingCtx) AfterFunc(f func()) func() bool {
	return func() bool { return false }
}

func (c *nonPropagatingCtx) cancel() {
	c.once.Do(func() { close(c.done) })
}
