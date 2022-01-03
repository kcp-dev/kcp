/*
Copyright 2022 The KCP Authors.

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
	"fmt"
	"testing"
	"time"
)

// TestingTInterface contains the methods that are implemented by both our T and testing.T
// Notably, methods for changing control flow (like Fatal) missing, since they do not function
// in a multi-goroutine context.
type TestingTInterface interface {
	Cleanup(f func())
	Deadline() (deadline time.Time, ok bool)
	Error(args ...interface{})
	Errorf(format string, args ...interface{})
	Failed() bool
	Helper()
	Log(args ...interface{})
	Logf(format string, args ...interface{})
	Name() string
	Parallel()
	Skip(args ...interface{})
	SkipNow()
	Skipf(format string, args ...interface{})
	Skipped() bool
	TempDir() string
}

func NewT(ctx context.Context, t *testing.T) *T {
	return &T{
		T:      t,
		ctx:    ctx,
		errors: make(chan error, 10),
	}
}

// T allows us to provide a similar UX to the testing.T while
// doing so in a multi-threaded context. The Go unit testing
// framework only allows the top-level goroutine to call FailNow
// so it's important to provide this interface to all the routines
// that want to be able to control the test execution flow.
type T struct {
	*testing.T
	ctx context.Context

	errors chan error
}

// the testing.T logger is not threadsafe...
func (t *T) Log(args ...interface{}) {
	t.T.Helper()
	t.T.Log(args...)
}

// the testing.T logger is not threadsafe...
func (t *T) Logf(format string, args ...interface{}) {
	t.T.Helper()
	t.T.Logf(format, args...)
}

func (t *T) Errorf(format string, args ...interface{}) {
	t.errors <- fmt.Errorf(format, args...)
}

// Wait receives data from producer threads and forwards it
// to the delegate; this call is blocking.
func (t *T) Wait() {
	t.T.Helper()
	for {
		select {
		case <-t.ctx.Done():
			return
		case err := <-t.errors:
			t.T.Helper()
			t.T.Error(err)
		}
	}
}

var _ TestingTInterface = &testing.T{}
var _ TestingTInterface = &T{}
