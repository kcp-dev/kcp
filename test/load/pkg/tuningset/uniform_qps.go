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

// NewUniformQPS creates a TuningSet that yields sequence numbers at a uniform rate.
// One sequence number is yielded every 1/qps seconds.
// Sequences can be partitioned using the start parameter.
func NewUniformQPS(qps float64, count, start int) TuningSet {
	return func(yield func(int) bool) {
		sleepDuration := time.Duration(float64(time.Second) / qps)
		for i := range count {
			if !yield(start + i) {
				return
			}
			if i < count-1 {
				time.Sleep(sleepDuration)
			}
		}
	}
}
