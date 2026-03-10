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
	"math/rand"
	"time"
)

// NewRandomizedQPS creates a TuningSet that yields sequence numbers at randomized intervals.
// The time between yields is drawn uniformly from [0, 2/averageQPS), making the
// average rate approximately averageQPS per second.
// Sequences can be partitioned using the start parameter.
func NewRandomizedQPS(averageQPS float64, count, start int) TuningSet {
	return func(yield func(int) bool) {
		maxSleep := 2.0 / averageQPS
		for i := range count {
			if !yield(start + i) {
				return
			}
			if i < count-1 {
				sleepDuration := time.Duration(rand.Float64() * maxSleep * float64(time.Second))
				time.Sleep(sleepDuration)
			}
		}
	}
}
