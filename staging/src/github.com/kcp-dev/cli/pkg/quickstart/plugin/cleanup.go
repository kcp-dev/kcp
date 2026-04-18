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

package plugin

import (
	"context"
	"errors"
	"fmt"

	"github.com/kcp-dev/cli/pkg/quickstart/scenarios"
)

// runCleanup runs each step's Cleanup function in reverse order.
// ctx must already carry a deadline — Run() sets one before calling here.
//
// execCtx.State is always empty here (not populated by a prior Execute pass).
// Cleanup funcs must rely only on values captured in their closures (e.g. the
// workspace name captured at Steps() call time), never on execCtx.State.
//
// Steps without a Cleanup func are skipped. Their resources are expected to be
// removed via kcp cascading deletion from a parent workspace. Sample resources
// (Cowboys) created with --with-samples live inside the consumer workspace and
// are cleaned up as part of that cascade.
func (o *QuickstartOptions) runCleanup(ctx context.Context, execCtx scenarios.ExecutionContext) error {
	fmt.Fprintf(o.Out, "Cleaning up quickstart resources with prefix %q...\n\n", o.NamePrefix)

	steps := o.scenario.Steps(o.NamePrefix)

	var errs []error
	var stepNumber int
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if step.Cleanup == nil {
			continue
		}
		stepNumber++

		desc := step.CleanupDescription
		if desc == "" {
			desc = step.Description
		}

		fmt.Fprintf(o.Out, "Step %d: %s...\n", stepNumber, desc)
		if err := step.Cleanup(ctx, execCtx); err != nil {
			fmt.Fprintf(o.ErrOut, "  cleanup of step %q failed: %v\n", desc, err)
			errs = append(errs, fmt.Errorf("cleanup of step %q: %w", desc, err))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	fmt.Fprintf(o.Out, "\nCleanup complete.\n")
	return nil
}
