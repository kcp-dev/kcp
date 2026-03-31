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

package framework

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/kcp/test/load/pkg/measurement"
	"github.com/kcp-dev/kcp/test/load/pkg/tuningset"
)

// TestExecuteWait verifies that Execute is blocking and waits for all actions to complete before returning.
func TestExecuteWait(t *testing.T) {
	ts := tuningset.NewUniformQPS(100, 5, 0)

	var completed int64
	action := func(seq int, sink measurement.Sink) error {
		time.Sleep(500 * time.Millisecond)
		atomic.AddInt64(&completed, 1)
		// ensure that errors are not messing with waiting behavior
		if seq%2 == 0 {
			return errors.New("test")
		}
		return nil
	}

	errs := Execute(ts, action, nil)

	require.Len(t, errs, 3) // 0, 2, 4
	require.Equal(t, int64(5), atomic.LoadInt64(&completed), "Execute must wait for all actions to complete before returning")
}
