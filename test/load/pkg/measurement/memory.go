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

import (
	"sync"

	"github.com/kcp-dev/kcp/test/load/pkg/stats"
)

var _ Sink = (*Memory)(nil)

// Memory is a simple in-memory backend that stores all measurements in a map.
// It allows for flexible calculations after a test run.
// Results will be returned as <stat name>_<original measurement name> (e.g. "avg_duration_ms").
type Memory struct {
	// Stats is a list of statistical functions to run on the measurements.
	// All Stats functions will be evaluated once you call Memory.Results().
	// Each NamedStat must have a unique Name which will be displayed in the results.
	Stats        []stats.NamedStat
	mu           sync.Mutex
	measurements map[string][]float64
}

// Drop simply stores the measurement in memory.
func (m *Memory) Drop(measurement Measurement) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.measurements == nil {
		m.measurements = make(map[string][]float64)
	}
	m.measurements[measurement.Name] = append(m.measurements[measurement.Name], measurement.Value)
}

// Results returns a map of calculated results for each measurement and stat.
func (m *Memory) Results() map[string]float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	results := make(map[string]float64, len(m.Stats)*len(m.measurements))

	for _, stat := range m.Stats {
		for mName, measurements := range m.measurements {
			results[stat.Name+"_"+mName] = stat.Calc(measurements)
		}
	}
	return results
}
