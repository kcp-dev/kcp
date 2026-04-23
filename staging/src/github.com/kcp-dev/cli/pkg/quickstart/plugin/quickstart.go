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
	"fmt"

	"github.com/kcp-dev/cli/pkg/quickstart/scenarios"
)

// Run executes the quickstart scenario, creating all resources step by step, or cleaning them up if --cleanup is set.
func (o *QuickstartOptions) Run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()

	execCtx, err := o.buildExecutionContext()
	if err != nil {
		return err
	}

	if o.Cleanup {
		if err := o.runCleanup(ctx, execCtx); err != nil {
			return err
		}
		if err := o.enterWorkspace(ctx, ":root"); err != nil {
			fmt.Fprintf(o.ErrOut, "Warning: cleanup succeeded but could not switch kubeconfig to :root: %v\n", err)
		}
		return nil
	}

	steps := o.scenario.Steps(o.NamePrefix)
	if o.WithSamples {
		steps = append(steps, o.scenario.Samples(o.NamePrefix)...)
	}

	fmt.Fprintf(o.Out, "Setting up %q scenario with prefix %q...\n\n",
		o.scenario.Name(), o.NamePrefix)

	for i, step := range steps {
		fmt.Fprintf(o.Out, "Step %d/%d: %s...\n", i+1, len(steps), step.Description)
		if err := step.Execute(ctx, execCtx); err != nil {
			return fmt.Errorf("step %d/%d failed: %w", i+1, len(steps), err)
		}
	}

	if err := o.scenario.PrintSummary(o.Out, o.NamePrefix, execCtx.State); err != nil {
		return err
	}

	if o.Enter {
		enterPath := o.scenario.EnterPath(execCtx.State)
		if enterPath == "" {
			return fmt.Errorf("scenario %q does not provide a workspace to enter", o.scenario.Name())
		}

		// NOTE: prepend ':' for the absolute-path syntax required by kubectl ws.
		if err := o.enterWorkspace(ctx, ":"+enterPath); err != nil {
			return fmt.Errorf("entering workspace: %w", err)
		}
	}

	return nil
}

func (o *QuickstartOptions) buildExecutionContext() (scenarios.ExecutionContext, error) {
	config, err := o.ClientConfig.ClientConfig()
	if err != nil {
		return scenarios.ExecutionContext{}, fmt.Errorf("failed to get client config: %w", err)
	}
	kcpClusterClient, err := o.newKCPClusterClient(config)
	if err != nil {
		return scenarios.ExecutionContext{}, fmt.Errorf("failed to create kcp client: %w", err)
	}
	dynamicClient, err := o.newKCPDynamicClient(config)
	if err != nil {
		return scenarios.ExecutionContext{}, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return scenarios.ExecutionContext{
		KCPClusterClient: kcpClusterClient,
		DynamicClient:    dynamicClient,
		Out:              o.Out,
		ErrOut:           o.ErrOut,
		State:            make(map[string]string),
	}, nil
}
