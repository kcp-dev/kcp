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

package stats

import (
	"github.com/montanaflynn/stats"
)

// NamedStats is a wrapper to give each stat calculation a name,
// we can use for printing results.
type NamedStat struct {
	Name string
	Calc func(values []float64) float64
}

func P99() NamedStat {
	return NamedStat{
		Name: "p99",
		Calc: func(values []float64) float64 {
			if len(values) == 0 {
				return 0
			}

			// we are safe to ignore the error here as we check
			// for null before and have the percentile on a fixed number
			p99, _ := stats.Percentile(values, 99)
			return p99
		},
	}
}

func Avg() NamedStat {
	return NamedStat{
		Name: "avg",
		Calc: func(values []float64) float64 {
			if len(values) == 0 {
				return 0
			}

			// we are safe to ignore the error here as we check
			// for null before
			mean, _ := stats.Mean(values)
			return mean
		},
	}
}
