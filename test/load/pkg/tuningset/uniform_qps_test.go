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

package tuningset

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/util/wait"
)

// TestUniformQPS verifies that actions are dispatched in parallel
// by checking that the total wall time matches the expected parallel duration.
func TestUniformQPS(t *testing.T) {
	t.Parallel()

	const count = 10
	const tolerance = 100 * time.Millisecond

	tests := []struct {
		name           string
		qps            float64
		actionDuration time.Duration
		expDuration    time.Duration
	}{
		{
			name:           "action slower than yield interval",
			qps:            20.0, // =50ms between yields
			actionDuration: 200 * time.Millisecond,
			expDuration:    650 * time.Millisecond, // (count - 1) * (1s/qps) + actionDuration
		},
		{
			name:           "action faster than yield interval",
			qps:            5.0, // =200ms between yields
			actionDuration: 50 * time.Millisecond,
			expDuration:    1850 * time.Millisecond, // (count - 1) * (1s/qps) + actionDuration
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := NewUniformQPS(tt.qps, count, 0)

			var wg wait.Group
			start := time.Now()
			for range ts {
				wg.Start(func() {
					time.Sleep(tt.actionDuration)
				})
			}
			wg.Wait()
			elapsed := time.Since(start)

			require.InDelta(t,
				tt.expDuration.Milliseconds(),
				elapsed.Milliseconds(),
				float64(tolerance.Milliseconds()),
				"elapsed time %v should be close to estimate %v",
				elapsed, tt.expDuration,
			)
		})
	}
}
