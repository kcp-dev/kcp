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
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRootCtx_ContextCreatesAndReturnsLive(t *testing.T) {
	t.Parallel()
	rc := newRootCtx(context.Background())

	ctx, _ := rc.context("k")
	if err := ctx.Err(); err != nil {
		t.Fatalf("expected live context, got Err=%v", err)
	}

	ctx2, _ := rc.context("k")
	if ctx2 != ctx {
		t.Fatalf("expected same context for same key, got different instances")
	}
}

func TestRootCtx_CancelIsSticky(t *testing.T) {
	t.Parallel()
	rc := newRootCtx(context.Background())

	ctx1, _ := rc.context("k")
	reason := errors.New("inactive")
	rc.cancel("k", reason)

	if err := ctx1.Err(); err == nil {
		t.Fatalf("expected first context to be cancelled, got nil")
	}
	if cause := context.Cause(ctx1); !errors.Is(cause, reason) {
		t.Fatalf("expected cancellation cause %v, got %v", reason, cause)
	}

	ctx2, _ := rc.context("k")
	if ctx2 != ctx1 {
		t.Fatalf("expected sticky cancelled context to be returned, got new instance")
	}
	if err := ctx2.Err(); err == nil {
		t.Fatalf("expected sticky context to be cancelled")
	}
}

func TestRootCtx_CancelBeforeContextCreatesTombstone(t *testing.T) {
	t.Parallel()
	rc := newRootCtx(context.Background())

	reason := errors.New("preemptive")
	rc.cancel("k", reason)

	ctx, _ := rc.context("k")
	if err := ctx.Err(); err == nil {
		t.Fatalf("expected tombstoned context to be already cancelled")
	}
	if cause := context.Cause(ctx); !errors.Is(cause, reason) {
		t.Fatalf("expected cancellation cause %v, got %v", reason, cause)
	}
}

func TestRootCtx_CancelIdempotent(t *testing.T) {
	t.Parallel()
	rc := newRootCtx(context.Background())

	ctx, _ := rc.context("k")
	rc.cancel("k", errors.New("first"))
	firstCause := context.Cause(ctx)

	rc.cancel("k", errors.New("second"))
	if got := context.Cause(ctx); got != firstCause {
		t.Fatalf("cancel should be idempotent: cause changed from %v to %v", firstCause, got)
	}
}

func TestRootCtx_Delete(t *testing.T) {
	t.Parallel()
	rc := newRootCtx(context.Background())

	ctx1, _ := rc.context("k")
	reason := errors.New("migrated")
	rc.delete("k", reason)

	if err := ctx1.Err(); err == nil {
		t.Fatalf("delete should cancel the removed context")
	}
	if cause := context.Cause(ctx1); !errors.Is(cause, reason) {
		t.Fatalf("expected cancellation cause %v, got %v", reason, cause)
	}

	ctx2, _ := rc.context("k")
	if ctx2 == ctx1 {
		t.Fatalf("expected fresh context after delete, got the deleted one")
	}
	if err := ctx2.Err(); err != nil {
		t.Fatalf("expected fresh live context after delete, got Err=%v", err)
	}

	// delete on a missing key is a no-op.
	rc.delete("missing", errors.New("nope"))
}

func TestRootCtx_CancelAll(t *testing.T) {
	t.Parallel()
	rc := newRootCtx(context.Background())

	ctxA, _ := rc.context("a")
	ctxB, _ := rc.context("b")

	reason := errors.New("shutdown")
	rc.cancelAll(reason)

	for _, c := range []context.Context{ctxA, ctxB} {
		if err := c.Err(); err == nil {
			t.Fatalf("expected derived context to be cancelled by cancelAll")
		}
	}
}

func TestRootCtx_ConcurrentCreateDeduplicates(t *testing.T) {
	t.Parallel()
	rc := newRootCtx(context.Background())

	const n = 64
	var wg sync.WaitGroup
	results := make([]context.Context, n)
	start := make(chan struct{})

	for i := range n {
		wg.Go(func() {
			<-start
			ctx, _ := rc.context("k")
			results[i] = ctx //nolint:fatcontext // required for the text
		})
	}
	close(start)
	wg.Wait()

	first := results[0]
	for i, c := range results {
		if c != first {
			t.Fatalf("expected all goroutines to receive the same context, mismatch at index %d", i)
		}
	}
}

func TestRootCtx_ConcurrentCancelAndContext(t *testing.T) {
	t.Parallel()
	rc := newRootCtx(context.Background())

	// Pre-create the entry so the race is purely between cancel and
	// concurrent reads, not against the slow-path create.
	rc.context("k")

	const readers = 32
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var raced atomic.Bool

	for range readers {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				rc.context("k")
			}
		})
	}

	// Let readers spin up.
	time.Sleep(10 * time.Millisecond)

	rc.cancel("k", errors.New("inactive"))

	// After cancel returns, every subsequent context call must yield a
	// cancelled context. Probe a bunch.
	for range 1000 {
		ctx, _ := rc.context("k")
		if ctx.Err() == nil {
			raced.Store(true)
			break
		}
	}

	close(stop)
	wg.Wait()

	if raced.Load() {
		t.Fatalf("context returned a non-cancelled ctx after cancel(k)")
	}
}
