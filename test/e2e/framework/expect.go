/*
Copyright 2021 The KCP Authors.

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

package framework

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
)

// Expectation closes over a statement of intent, allowing the caller
// to accumulate errors and determine when the expectation should cease
// to be evaluated.
type Expectation func(ctx context.Context) (done bool, err error)

// Expecter allows callers to register expectations
type Expecter interface {
	// ExpectBefore will result in the Expectation being evaluated whenever
	// state changes, up until the desired timeout is reached.
	ExpectBefore(context.Context, Expectation, time.Duration) error
}

// NewExpecter creates a informer-driven registry of expectations, which will
// be triggered on every event that the informer ingests.
func NewExpecter(informer cache.SharedIndexInformer) *ExpectationController {
	controller := ExpectationController{
		expectations: map[uuid.UUID]expectationRecord{},
		lock:         sync.RWMutex{},
	}

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(_ interface{}) {
			controller.triggerExpectations()
		},
		UpdateFunc: func(_, _ interface{}) {
			controller.triggerExpectations()
		},
		DeleteFunc: func(_ interface{}) {
			controller.triggerExpectations()
		},
	})

	return &controller
}

type expectationRecord struct {
	evaluate func()
	// calls to evaluate this expectation from the informer's event handlers
	// will be concurrent with the call we make during expectation registration,
	// so we need to synchronize them to ensure that downstream consumers of this
	// library do not need to worry about synchronization if their functions have
	// mutating side-effects
	*sync.Mutex
}

// ExpectationController knows how to register expectations and trigger them
type ExpectationController struct {
	// expectations are recorded by UUID so they may be removed after they complete
	expectations map[uuid.UUID]expectationRecord
	lock         sync.RWMutex
}

var _ Expecter = &ExpectationController{}

func (c *ExpectationController) triggerExpectations() {
	c.lock.RLock()
	defer c.lock.RUnlock()

	for id := range c.expectations {
		c.expectations[id].Lock()
		c.expectations[id].evaluate()
		c.expectations[id].Unlock()
	}
}

func (c *ExpectationController) ExpectBefore(ctx context.Context, expectation Expectation, duration time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	type result struct {
		done bool
		err  error
	}
	results := make(chan result)

	// producer wraps the expectation and allows the informer-driven flow to trigger
	// it while the side effects of the call feed the channel we listen to here.
	expectationCtx, expectationCancel := context.WithCancel(ctx)
	producer := func() {
		done, err := expectation(expectationCtx)
		if expectationCtx.Err() == nil {
			results <- result{
				done: done,
				err:  err,
			}
		}
	}

	id := uuid.New()
	c.lock.Lock()
	c.expectations[id] = expectationRecord{
		evaluate: producer,
		Mutex:    &sync.Mutex{},
	}
	c.lock.Unlock()

	defer func() {
		// We use an unbuffered channel for expectation results, so we need to make sure that
		// no producer of results is stuck writing to the channel once we're done reading it,
		// whether that be because the expectation is done or the deadline has passed.

		// First, signal to all future executions of the expectation to not send any new
		// results to the channel.
		expectationCancel()

		// Then, drain the channel of all results.
		wg := sync.WaitGroup{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range results {
			} // we can throw away these results
		}()

		// Finally, acquire the lock to close the results channel and deregister the expectation.
		// Acquiring the lock requires that triggerExpectations() is not active with the read lock,
		// so we know that no new expectations will be triggered after we acquire this.
		c.lock.Lock()
		close(results)
		wg.Wait()
		delete(c.expectations, id)
		c.lock.Unlock()
	}()

	// evaluate once to get the current state once we're registered to see future events
	go func() {
		c.lock.RLock()
		// It's possible that this first evaluation races with us getting called from the
		// controller, so we need to make sure that there's still an expectation registered
		// here before running it!
		if e, ok := c.expectations[id]; ok {
			e.Lock()
			e.evaluate()
			e.Unlock()
		}
		c.lock.RUnlock()
	}()

	var expectationErrors []error
	var processed int
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("expected state not found: %w, %d errors encountered while processing %d events: %v", ctx.Err(), len(expectationErrors), processed, kerrors.NewAggregate(expectationErrors))
		case result := <-results:
			processed += 1
			if result.err != nil {
				expectationErrors = append(expectationErrors, result.err)
			}
			if result.done {
				if result.err == nil {
					return nil
				}
				return kerrors.NewAggregate(expectationErrors)
			}
		}
	}
}

// The following are statically-typed helpers for common types to allow us to express expectations about objects.

// RegisterWorkspaceExpectation registers an expectation about the future state of the seed.
type RegisterWorkspaceExpectation func(seed *tenancyv1beta1.Workspace, expectation WorkspaceExpectation) error

// WorkspaceExpectation evaluates an expectation about the object.
type WorkspaceExpectation func(*tenancyv1beta1.Workspace) error

// ExpectWorkspaces sets up an Expecter in order to allow registering expectations in tests with minimal setup.
func ExpectWorkspaces(
	ctx context.Context,
	t *testing.T,
	client kcpclient.Interface,
) (RegisterWorkspaceExpectation, error) {
	kcpSharedInformerFactory := kcpinformers.NewSharedScopedInformerFactoryWithOptions(client, 0)
	workspaceInformer := kcpSharedInformerFactory.Tenancy().V1beta1().Workspaces()
	expecter := NewExpecter(workspaceInformer.Informer())
	kcpSharedInformerFactory.Start(ctx.Done())
	waitCtx, cancel := context.WithTimeout(ctx, wait.ForeverTestTimeout)
	t.Cleanup(cancel)
	if !cache.WaitForNamedCacheSync(t.Name(), waitCtx.Done(), workspaceInformer.Informer().HasSynced) {
		return nil, errors.New("failed to wait for caches to sync")
	}
	return func(seed *tenancyv1beta1.Workspace, expectation WorkspaceExpectation) error {
		return expecter.ExpectBefore(ctx, func(ctx context.Context) (done bool, err error) {
			current, err := workspaceInformer.Lister().Get(seed.Name)
			if err != nil {
				return !apierrors.IsNotFound(err), err
			}
			expectErr := expectation(current.DeepCopy())
			return expectErr == nil, expectErr
		}, wait.ForeverTestTimeout)
	}, nil
}

// RegisterWorkspaceShardExpectation registers an expectation about the future state of the seed.
type RegisterWorkspaceShardExpectation func(seed *corev1alpha1.Shard, expectation WorkspaceShardExpectation) error

// WorkspaceShardExpectation evaluates an expectation about the object.
type WorkspaceShardExpectation func(*corev1alpha1.Shard) error

// ExpectWorkspaceShards sets up an Expecter in order to allow registering expectations in tests with minimal setup.
func ExpectWorkspaceShards(ctx context.Context, t *testing.T, client kcpclient.Interface) (RegisterWorkspaceShardExpectation, error) {
	kcpSharedInformerFactory := kcpinformers.NewSharedScopedInformerFactoryWithOptions(client, 0)
	workspaceShardInformer := kcpSharedInformerFactory.Core().V1alpha1().Shards()
	expecter := NewExpecter(workspaceShardInformer.Informer())
	kcpSharedInformerFactory.Start(ctx.Done())
	waitCtx, cancel := context.WithTimeout(ctx, wait.ForeverTestTimeout)
	t.Cleanup(cancel)
	if !cache.WaitForNamedCacheSync(t.Name(), waitCtx.Done(), workspaceShardInformer.Informer().HasSynced) {
		return nil, errors.New("failed to wait for caches to sync")
	}
	return func(seed *corev1alpha1.Shard, expectation WorkspaceShardExpectation) error {
		return expecter.ExpectBefore(ctx, func(ctx context.Context) (done bool, err error) {
			current, err := workspaceShardInformer.Lister().Get(seed.Name)
			if err != nil {
				return !apierrors.IsNotFound(err), err
			}
			expectErr := expectation(current.DeepCopy())
			return expectErr == nil, expectErr
		}, wait.ForeverTestTimeout)
	}, nil
}
