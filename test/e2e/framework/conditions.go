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
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

var _ TestingTInterface = (*withoutErrorAndFailT)(nil)

type withoutErrorAndFailT struct {
	delegate TestingTInterface
}

func (t *withoutErrorAndFailT) Cleanup(f func())                          { t.delegate.Cleanup(f) }
func (t *withoutErrorAndFailT) Deadline() (deadline time.Time, ok bool)   { return t.delegate.Deadline() }
func (t *withoutErrorAndFailT) Error(args ...interface{})                 {}
func (t *withoutErrorAndFailT) Errorf(format string, args ...interface{}) {}
func (t *withoutErrorAndFailT) Failed() bool                              { return t.delegate.Failed() }
func (t *withoutErrorAndFailT) Helper()                                   { t.delegate.Helper() }
func (t *withoutErrorAndFailT) Log(args ...interface{})                   { t.delegate.Log(args) }
func (t *withoutErrorAndFailT) Logf(format string, args ...interface{})   { t.delegate.Log(args) }
func (t *withoutErrorAndFailT) Name() string                              { return t.delegate.Name() }
func (t *withoutErrorAndFailT) Parallel()                                 { t.delegate.Parallel() }
func (t *withoutErrorAndFailT) Skip(args ...interface{})                  { t.delegate.Skip(args) }
func (t *withoutErrorAndFailT) SkipNow()                                  { t.delegate.SkipNow() }
func (t *withoutErrorAndFailT) Skipf(format string, args ...interface{}) {
	t.delegate.Skipf(format, args)
}
func (t *withoutErrorAndFailT) Skipped() bool   { return t.delegate.Skipped() }
func (t *withoutErrorAndFailT) TempDir() string { return t.delegate.TempDir() }

func WaitForCondition(t TestingTInterface, ctx context.Context, timeout time.Duration, test func(context.Context, TestingTInterface) (bool, error)) (bool, error) {
	succeeded := false
	var err error
	waitContext, cancelWait := context.WithTimeout(ctx, timeout)
	withoutErrorAndFailT := &withoutErrorAndFailT{t}
	wait.UntilWithContext(waitContext, func(ctx context.Context) {
		var conditionMet bool
		conditionMet, err = test(ctx, withoutErrorAndFailT)
		if err != nil {
			cancelWait()
			return
		}
		if conditionMet {
			succeeded = true
			cancelWait()
			return
		}
	}, 100*time.Millisecond)
	if err != nil {
		return false, err
	}
	if !succeeded {
		return test(ctx, t)
	}

	return true, nil
}
