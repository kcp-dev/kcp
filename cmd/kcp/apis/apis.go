/*
Copyright 2024 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package apis

import (
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// NewCmdAPIs creates the apis command with subcommands for managing APIBindings
func NewCmdAPIs(streams genericclioptions.IOStreams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apis",
		Short: "Manage API bindings",
		Long:  "Commands for managing APIBindings and their PermissionClaims",
	}

	cmd.AddCommand(NewCmdBind(streams))

	return cmd
}
