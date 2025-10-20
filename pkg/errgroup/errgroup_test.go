/*
Copyright 2025 The kcp Authors.

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

package errgroup

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroup_NoContext_NoRunners(t *testing.T) {
	var g Group
	err := g.Wait()
	require.NoError(t, err)
}

func TestGroup_WithContext_NoRunners(t *testing.T) {
	g := WithContext(context.Background())
	require.NotNil(t, g)
	err := g.Wait()
	require.NoError(t, err)
}

func TestGroup_NoContext_NilRunner(t *testing.T) {
	var g Group
	g.Go(nil)
	err := g.Wait()
	require.NoError(t, err)
}

func TestGroup_WithContext_NilRunner(t *testing.T) {
	g := WithContext(context.Background())
	g.Go(nil)
	err := g.Wait()
	require.NoError(t, err)
}

func TestGroup_NoContext_SuccessfulRunners(t *testing.T) {
	var g Group
	var count atomic.Int32

	for range 5 {
		g.Go(func(_ context.Context) error {
			count.Add(1)
			return nil
		})
	}

	err := g.Wait()
	require.NoError(t, err)
	assert.Equal(t, int32(5), count.Load())
}

func TestGroup_WithContext_SuccessfulRunners(t *testing.T) {
	g := WithContext(context.Background())
	var count atomic.Int32

	for range 5 {
		g.Go(func(_ context.Context) error {
			count.Add(1)
			return nil
		})
	}

	err := g.Wait()
	require.NoError(t, err)
	assert.Equal(t, int32(5), count.Load())
}

func TestGroup_NoContext_SingleError(t *testing.T) {
	var g Group
	expected := errors.New("test error")

	g.Go(func(_ context.Context) error {
		return expected
	})

	err := g.Wait()
	require.Error(t, err)
	assert.Contains(t, err.Error(), expected.Error())
}

func TestGroup_WithContext_SingleError(t *testing.T) {
	g := WithContext(context.Background())
	expected := errors.New("test error")

	g.Go(func(_ context.Context) error {
		return expected
	})

	err := g.Wait()
	require.Error(t, err)
	assert.Contains(t, err.Error(), expected.Error())
}

func TestGroup_NoContext_MultipleErrors(t *testing.T) {
	var g Group
	err1 := errors.New("error one")
	err2 := errors.New("error two")

	g.Go(func(_ context.Context) error { return err1 })
	g.Go(func(_ context.Context) error { return err2 })

	err := g.Wait()
	require.Error(t, err)
	assert.Contains(t, err.Error(), err1.Error())
	assert.Contains(t, err.Error(), err2.Error())
}

func TestGroup_WithContext_MultipleErrors(t *testing.T) {
	g := WithContext(context.Background())
	err1 := errors.New("error one")
	err2 := errors.New("error two")

	g.Go(func(_ context.Context) error { return err1 })
	g.Go(func(_ context.Context) error { return err2 })

	err := g.Wait()
	require.Error(t, err)
	assert.Contains(t, err.Error(), err1.Error())
	assert.Contains(t, err.Error(), err2.Error())
}

func TestGroup_NoContext_MixedResults(t *testing.T) {
	var g Group
	expected := errors.New("some error")
	var okCount atomic.Int32

	g.Go(func(_ context.Context) error {
		okCount.Add(1)
		return nil
	})
	g.Go(func(_ context.Context) error {
		return expected
	})
	g.Go(func(_ context.Context) error {
		okCount.Add(1)
		return nil
	})

	err := g.Wait()
	require.Error(t, err)
	assert.Contains(t, err.Error(), expected.Error())
	assert.Equal(t, int32(2), okCount.Load())
}

func TestGroup_WithContext_MixedResults(t *testing.T) {
	g := WithContext(context.Background())
	expected := errors.New("some error")
	var okCount atomic.Int32

	g.Go(func(_ context.Context) error {
		okCount.Add(1)
		return nil
	})
	g.Go(func(_ context.Context) error {
		return expected
	})
	g.Go(func(_ context.Context) error {
		okCount.Add(1)
		return nil
	})

	err := g.Wait()
	require.Error(t, err)
	assert.Contains(t, err.Error(), expected.Error())
	assert.Equal(t, int32(2), okCount.Load())
}

func TestGroup_WithContext_ContextPassedToRunners(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "hello")
	g := WithContext(ctx)

	var got atomic.Value
	g.Go(func(ctx context.Context) error {
		got.Store(ctx.Value(key{}))
		return nil
	})

	err := g.Wait()
	require.NoError(t, err)
	assert.Equal(t, "hello", got.Load())
}

func TestGroup_NoContext_RunnersGetBackgroundDerivedContext(t *testing.T) {
	var g Group

	var gotCtx atomic.Value
	g.Go(func(ctx context.Context) error {
		gotCtx.Store(ctx)
		return nil
	})

	err := g.Wait()
	require.NoError(t, err)
	require.NotNil(t, gotCtx.Load())
}

func TestGroup_WithContext_CancelledParentContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	g := WithContext(ctx)

	var gotErr atomic.Value
	g.Go(func(ctx context.Context) error {
		<-ctx.Done()
		gotErr.Store(ctx.Err())
		return ctx.Err()
	})

	err := g.Wait()
	require.Error(t, err)
	assert.ErrorIs(t, gotErr.Load().(error), context.Canceled)
}

func TestGroup_NoContext_FailFast(t *testing.T) {
	var g Group
	g.FailFast = true

	started := make(chan struct{})
	g.Go(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})

	// Ensure the first goroutine is running before triggering the error.
	<-started

	g.Go(func(_ context.Context) error {
		return errors.New("fail")
	})

	err := g.Wait()
	require.Error(t, err)
}

func TestGroup_WithContext_FailFast(t *testing.T) {
	g := WithContext(context.Background())
	g.FailFast = true

	started := make(chan struct{})
	g.Go(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})

	// Ensure the first goroutine is running before triggering the error.
	<-started

	g.Go(func(_ context.Context) error {
		return errors.New("fail")
	})

	err := g.Wait()
	require.Error(t, err)
}

func TestGroup_WithContext_FailFastCancelsContext(t *testing.T) {
	g := WithContext(context.Background())
	g.FailFast = true

	cancelled := make(chan struct{})
	g.Go(func(ctx context.Context) error {
		<-ctx.Done()
		close(cancelled)
		return nil
	})

	g.Go(func(_ context.Context) error {
		return errors.New("trigger fail fast")
	})

	err := g.Wait()
	require.Error(t, err)

	// The cancelled channel should be closed because fail-fast
	// cancels the context.
	select {
	case <-cancelled:
		// expected
	case <-time.After(time.Second):
		t.Fatal("expected context to be cancelled by fail fast")
	}
}

func TestGroup_NoContext_FailFastFalseDoesNotCancel(t *testing.T) {
	var g Group
	g.FailFast = false

	var completed atomic.Bool
	g.Go(func(ctx context.Context) error {
		// Small sleep to ensure the error goroutine finishes first.
		time.Sleep(50 * time.Millisecond)
		completed.Store(true)
		return nil
	})

	g.Go(func(_ context.Context) error {
		return errors.New("error")
	})

	err := g.Wait()
	require.Error(t, err)
	assert.True(t, completed.Load(), "goroutine should complete even after another errors")
}

func TestGroup_WithContext_FailFastFalseDoesNotCancel(t *testing.T) {
	g := WithContext(context.Background())
	g.FailFast = false

	var completed atomic.Bool
	g.Go(func(ctx context.Context) error {
		// Small sleep to ensure the error goroutine finishes first.
		time.Sleep(50 * time.Millisecond)
		completed.Store(true)
		return nil
	})

	g.Go(func(_ context.Context) error {
		return errors.New("error")
	})

	err := g.Wait()
	require.Error(t, err)
	assert.True(t, completed.Load(), "goroutine should complete even after another errors")
}

func TestGroup_WaitCalledMultipleTimes(t *testing.T) {
	var g Group
	g.Go(func(_ context.Context) error {
		return errors.New("err")
	})

	err1 := g.Wait()
	err2 := g.Wait()
	require.Error(t, err1)
	assert.Equal(t, err1.Error(), err2.Error(), "wait should return the same error on repeated calls")
}

func TestGroup_NilGroup_Wait(t *testing.T) {
	// A zero-value Group should work without explicit initialization.
	g := &Group{}
	err := g.Wait()
	require.NoError(t, err)
}

func TestGroup_NilGroup_GoNil(t *testing.T) {
	g := &Group{}
	g.Go(nil)
	err := g.Wait()
	require.NoError(t, err)
}

func TestGroup_WithContext_WaitCancelsContext(t *testing.T) {
	ctx := context.Background()
	g := WithContext(ctx)

	var runnerCtx atomic.Value
	g.Go(func(ctx context.Context) error {
		runnerCtx.Store(ctx)
		return nil
	})

	err := g.Wait()
	require.NoError(t, err)

	storedCtx := runnerCtx.Load().(context.Context)
	assert.Error(t, storedCtx.Err(), "context should be cancelled after Wait returns")
}
