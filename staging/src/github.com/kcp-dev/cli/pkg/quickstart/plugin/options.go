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
	"time"

	"github.com/spf13/cobra"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/cli/pkg/base"
	"github.com/kcp-dev/cli/pkg/quickstart/scenarios"
	workspaceplugin "github.com/kcp-dev/cli/pkg/workspace/plugin"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
)

type QuickstartOptions struct {
	*base.Options
	Scenario    string
	NamePrefix  string
	Cleanup     bool
	Enter       bool
	WithSamples bool
	Timeout time.Duration

	scenario            scenarios.Scenario
	enterWorkspace      func(ctx context.Context, path string) error
	newUseWorkspaceOpts func(genericclioptions.IOStreams) *workspaceplugin.UseWorkspaceOptions
	newKCPClusterClient func(config *rest.Config) (kcpclientset.ClusterInterface, error)
	newKCPDynamicClient func(config *rest.Config) (kcpdynamic.ClusterInterface, error)
}

func NewQuickstartOptions(streams genericclioptions.IOStreams) *QuickstartOptions {
	o := &QuickstartOptions{
		Options:             base.NewOptions(streams),
		Scenario:            "api-provider",
		NamePrefix:          "quickstart",
		Timeout:             10 * time.Minute,
		newUseWorkspaceOpts: workspaceplugin.NewUseWorkspaceOptions,
		newKCPClusterClient: defaultKCPClusterClient,
		newKCPDynamicClient: defaultKCPDynamicClient,
	}
	o.enterWorkspace = o.defaultEnterWorkspace
	return o
}

func (o *QuickstartOptions) defaultEnterWorkspace(ctx context.Context, path string) error {
	useOpts := o.newUseWorkspaceOpts(o.IOStreams)
	useOpts.Options = o.Options
	if err := useOpts.Complete([]string{path}); err != nil {
		return err
	}
	return useOpts.Run(ctx)
}

func (o *QuickstartOptions) BindFlags(cmd *cobra.Command) {
	o.Options.BindFlags(cmd)
	cmd.Flags().StringVar(&o.Scenario, "scenario", o.Scenario,
		"Scenario to bootstrap (api-provider)")
	cmd.Flags().StringVar(&o.NamePrefix, "name-prefix", o.NamePrefix,
		"Prefix for created workspace names")
	cmd.Flags().BoolVar(&o.Cleanup, "cleanup", o.Cleanup,
		"Delete all resources created by a previous quickstart run. "+
			"Relies on kcp cascading deletion from the org workspace — "+
			"APIResourceSchemas, APIExports, and APIBindings inside child workspaces "+
			"are removed as part of the cascade, not individually.")
	cmd.Flags().BoolVar(&o.Enter, "enter", o.Enter,
		"Switch kubeconfig to the consumer workspace when done")
	cmd.Flags().BoolVar(&o.WithSamples, "with-samples", o.WithSamples,
		"Apply sample resources (Cowboys) into the consumer workspace after setup")
	cmd.Flags().DurationVar(&o.Timeout, "timeout", o.Timeout,
		"Maximum time to wait for workspaces to become ready or finish terminating")
}

func (o *QuickstartOptions) Complete(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("unexpected arguments: %v", args)
	}

	if err := o.Options.Complete(); err != nil {
		return fmt.Errorf("failed to initialize ClientConfig: %w", err)
	}

	s, err := scenarios.Get(o.Scenario)
	if err != nil {
		return fmt.Errorf("failed to get scenario: %w", err)
	}

	o.scenario = s

	return nil
}

func (o *QuickstartOptions) Validate() error {
	if err := o.Options.Validate(); err != nil {
		return fmt.Errorf("failed to validate options: %w", err)
	}

	if o.scenario == nil {
		return fmt.Errorf("scenario not initialised; Complete() must be called before Validate()")
	}

	if o.NamePrefix == "" {
		return fmt.Errorf("--name-prefix must not be empty")
	}

	return nil
}
