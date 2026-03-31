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

// Sink collects measurements during a load test. Implementations must be
// safe for concurrent use, as Execute calls Drop from multiple goroutines.
type Sink interface {
	// Drop adds a measurement to the backend. The backend is responsible for handling the measurement and calculating results when requested.
	Drop(measurement Measurement)
	// Results calculates and returns the results of all measurements. The results are returned as a map where the key is the name of the measurement and the value is the calculated result.
	Results() map[string]float64
}

type Measurement struct {
	Name  string
	Value float64
}
