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
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/util/wait"
)

// TestSteppedLoad verifies burst and step timing.
func TestSteppedLoad(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		burstSize int
		stepDelay time.Duration
		count     int
	}{
		{
			name:      "3 full bursts",
			burstSize: 3,
			stepDelay: 200 * time.Millisecond,
			count:     9, // 3 bursts of 3, 2 pauses
		},
		{
			name:      "partial last burst",
			burstSize: 4,
			stepDelay: 200 * time.Millisecond,
			count:     10, // 2 full bursts of 4 + 1 partial burst of 2, 2 pauses
		},
		{
			name:      "single element bursts",
			burstSize: 1,
			stepDelay: 100 * time.Millisecond,
			count:     5, // 5 bursts of 1, 4 pauses
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ts := NewSteppedLoad(tt.burstSize, tt.stepDelay, tt.count, 0)

				// Record the timestamp of each yield to verify burst grouping.
				yieldTimes := make([]time.Time, 0, tt.count)
				for range ts {
					yieldTimes = append(yieldTimes, time.Now())
				}
				require.Len(t, yieldTimes, tt.count)

				// Verify items within the same burst are yielded instantly
				// and that step delays appear between bursts.
				for i := 1; i < tt.count; i++ {
					gap := yieldTimes[i].Sub(yieldTimes[i-1])
					isBurstBoundary := i%tt.burstSize == 0

					if isBurstBoundary {
						require.Equal(t, tt.stepDelay, gap,
							"gap between item %d and %d should be exactly stepDelay (%v), got %v",
							i-1, i, tt.stepDelay, gap,
						)
					} else {
						require.Zero(t, gap,
							"gap within burst between item %d and %d should be 0, got %v",
							i-1, i, gap,
						)
					}
				}
			})
		})
	}
}

// TestSteppedLoadParallel verifies that actions within a burst are dispatched
// in parallel by checking that the total wall time reflects burst parallelism.
func TestSteppedLoadParallel(t *testing.T) {
	t.Parallel()

	const (
		burstSize      = 5
		stepDelay      = 100 * time.Millisecond
		count          = 15 // 3 bursts, 2 pauses
		actionDuration = 150 * time.Millisecond
	)

	// With parallel execution inside bursts:
	// burst1 yields instantly, all 5 actions start concurrently
	// stepDelay pause
	// burst2 yields instantly, all 5 actions start concurrently
	// stepDelay pause
	// burst3 yields instantly, all 5 actions start concurrently
	//
	// Total wall time = 2*stepDelay + actionDuration = 200ms + 150ms = 350ms
	numPauses := (count - 1) / burstSize
	expDuration := time.Duration(numPauses)*stepDelay + actionDuration

	synctest.Test(t, func(t *testing.T) {
		ts := NewSteppedLoad(burstSize, stepDelay, count, 0)

		var wg wait.Group
		start := time.Now()
		for range ts {
			wg.Start(func() {
				time.Sleep(actionDuration)
			})
		}
		wg.Wait()
		elapsed := time.Since(start)

		require.Equal(t, expDuration, elapsed,
			"elapsed time %v should equal parallel estimate %v",
			elapsed, expDuration,
		)
	})
}
