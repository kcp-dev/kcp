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

// TestRandomizedQPS verifies that actions are dispatched in parallel
// by checking that the total wall time stays within the expected bounds.
// Because the sleep between yields is randomized (uniform in [0, 2/averageQPS)),
// we use bounds rather than a point estimate:
//
// All intervals are set to prove parallel execution, since sequential execution
// would be much slower than the upper bound.
func TestRandomizedQPS(t *testing.T) {
	t.Parallel()

	const count = 10
	const tolerance = 50 * time.Millisecond

	tests := []struct {
		name           string
		averageQPS     float64
		actionDuration time.Duration
	}{
		{
			name:           "action slower than average yield interval",
			averageQPS:     20.0, // avg 50ms between yields, max 100ms
			actionDuration: 200 * time.Millisecond,
		},
		{
			name:           "action faster than average yield interval",
			averageQPS:     5.0, // avg 200ms between yields, max 400ms
			actionDuration: 50 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			maxSleep := time.Duration(2.0 / tt.averageQPS * float64(time.Second))
			lowerBound := tt.actionDuration
			upperBound := time.Duration(count-1)*maxSleep + tt.actionDuration

			ts := NewRandomizedQPS(tt.averageQPS, count, 0)

			var wg wait.Group
			start := time.Now()
			for range ts {
				wg.Start(func() {
					time.Sleep(tt.actionDuration)
				})
			}
			wg.Wait()
			elapsed := time.Since(start)

			// Sequential would be at least: count * actionDuration + total sleeps
			// which is always > upperBound, so being within bounds proves parallelism as well
			require.GreaterOrEqual(t, elapsed.Milliseconds(), (lowerBound - tolerance).Milliseconds(),
				"elapsed time %v should be >= lower bound %v (minus tolerance)", elapsed, lowerBound)
			require.LessOrEqual(t, elapsed.Milliseconds(), (upperBound + tolerance).Milliseconds(),
				"elapsed time %v should be <= upper bound %v (plus tolerance)", elapsed, upperBound)
		})
	}
}
