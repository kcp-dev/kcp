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
	"iter"

	"github.com/kcp-dev/kcp/test/load/pkg/measurement"
)

// Action is a function that performs a single load test operation.
// seq is the sequence number of the action.
// sink is the measurement sink for recording metrics.
type Action func(seq int, sink measurement.Sink) error

// TuningSet is an iterator that yields sequence numbers according to a
// timing pattern. It controls when each action is started by blocking
// between yields. TuningSets implementations have to be finite.
type TuningSet iter.Seq[int]
