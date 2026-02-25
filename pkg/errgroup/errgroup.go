/*
Copyright 2025 The KCP Authors.

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
	"sync"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
)

// Group allows running a number of [Runner] concurrently and
// aggregating their errors.
//
// Using the default value of a Group is a valid use.
//
// Its interface is inspired by the stdlib experiement errgroup:
// https://pkg.go.dev/golang.org/x/sync@v0.19.0/errgroup
type Group struct {
	// FailFast causes the Group to cancel all goroutines immediately
	// when one goroutine returns an error.
	// FailFast must be set before any [Runner] is started.
	FailFast bool

	wg sync.WaitGroup

	lock    sync.Mutex
	ctxOnce func() (context.Context, context.CancelFunc)
	errs    []error
}

// WithContext returns a new Group.
//
// A group instantiated this way should only live for the lifetime of
// the calling function as this stores the context, which can cause
// unintended side effects.
func WithContext(ctx context.Context) *Group {
	g := new(Group)
	_, _ = g.context(ctx)
	return g
}

func (g *Group) context(ctx context.Context) (context.Context, context.CancelFunc) {
	g.lock.Lock()
	defer g.lock.Unlock()
	if g.ctxOnce == nil {
		g.ctxOnce = sync.OnceValues(func() (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		})
	}
	return g.ctxOnce()
}

// Runner is a function that can be executed by a [Grouo].
type Runner func(context.Context) error

// Go starts a [Runner] in a goroutine bound by the [context.Context]
// the Group was initialized with.
func (g *Group) Go(runner Runner) {
	if runner == nil {
		return
	}

	ctx, _ := g.context(context.Background())

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		g.addError(runner(ctx))
	}()
}

func (g *Group) addError(err error) {
	if err == nil {
		return
	}

	if g.FailFast {
		_, cancel := g.context(context.Background())
		cancel()
	}

	g.lock.Lock()
	defer g.lock.Unlock()

	if g.errs == nil {
		g.errs = []error{err}
		return
	}
	g.errs = append(g.errs, err)
}

// Wait waits until all [Runner] started with [Group.Go] have finished
// and returns an aggregate of their errors.
func (g *Group) Wait() error {
	g.wg.Wait()

	// Cancelling the context just to cancel it. By this point it is
	// either cancelled (because an error occurred) or all Runners are
	// finished.
	_, cancel := g.context(context.Background())
	cancel()

	return utilerrors.NewAggregate(g.errs)
}
