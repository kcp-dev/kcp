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

package measurement

import "time"

// RecordElapsedDurationMS is a convenience function to handle duration measurements.
// You can call it in your test using:
//
//	defer measurement.RecordElapsedDurationMS(time.Now(), sink)
func RecordElapsedDurationMS(start time.Time, sink Sink) {
	duration := time.Since(start)
	sink.Drop(Measurement{Name: "duration_ms", Value: duration.Seconds() * 1000})
}
