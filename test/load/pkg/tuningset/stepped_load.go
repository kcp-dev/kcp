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

import "time"

// NewSteppedLoad creates a TuningSet that yields sequence numbers in bursts.
// It yields burstSize numbers, then waits stepDelay before the next burst.
// Sequences can be partitioned using the start parameter.
func NewSteppedLoad(burstSize int, stepDelay time.Duration, count, start int) TuningSet {
	return func(yield func(int) bool) {
		for i := range count {
			if !yield(start + i) {
				return
			}
			// Sleep after completing a burst (but not after the last element)
			if i < count-1 && (i+1)%burstSize == 0 {
				time.Sleep(stepDelay)
			}
		}
	}
}
