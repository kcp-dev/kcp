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

package framework

import (
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/kcp/test/load/pkg/measurement"
	"github.com/kcp-dev/kcp/test/load/pkg/tuningset"
)

// Execute runs the given action for every sequence number yielded by the
// TuningSet. Execute blocks until all actions complete and returns all
// collected errors.
func Execute(ts tuningset.TuningSet, action tuningset.Action, sink measurement.Sink) []error {
	var mu sync.Mutex
	var errs []error

	var wg wait.Group
	for seq := range ts {
		wg.Start(func() {
			if err := action(seq, sink); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("seq %d: %w", seq, err))
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return errs
}
