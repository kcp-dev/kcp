/*
Copyright 2024 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package apis

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned"
)

// BindOptions contains the options for binding to an APIExport
type BindOptions struct {
	genericclioptions.IOStreams
	ConfigFlags *genericclioptions.ConfigFlags

	Name   string
	Export string
}

// NewCmdBind creates the bind command
func NewCmdBind(streams genericclioptions.IOStreams) *cobra.Command {
	o := &BindOptions{
		IOStreams:   streams,
		ConfigFlags: genericclioptions.NewConfigFlags(true),
	}

	cmd := &cobra.Command{
		Use:   "bind NAME --export=EXPORT_NAME",
		Short: "Create a binding to an APIExport",
		Long:  "Create a new APIBinding that references an APIExport",
		Example: `  # Bind to an APIExport
  kcp apis bind my-binding --export=my-export
  
  # Bind to an APIExport in a different workspace
  kcp apis bind my-binding --export=root:org:my-export`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return fmt.Errorf("NAME is required")
			}
			o.Name = args[0]
			return o.Run(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&o.Export, "export", "", "APIExport to bind to (required)")
	cmd.MarkFlagRequired("export")
	o.ConfigFlags.AddFlags(cmd.Flags())

	return cmd
}

// Run executes the bind command
func (o *BindOptions) Run(ctx context.Context) error {
	config, err := o.ConfigFlags.ToRESTConfig()
	if err != nil {
		return fmt.Errorf("failed to get REST config: %w", err)
	}

	client, err := kcpclientset.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create KCP client: %w", err)
	}

	binding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: o.Name,
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Name: o.Export,
				},
			},
		},
	}

	created, err := client.ApisV1alpha1().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create APIBinding: %w", err)
	}

	fmt.Fprintf(o.Out, "APIBinding %q created successfully\n", created.Name)
	return nil
}
