/*
Copyright 2022 The KCP Authors.

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

package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"k8s.io/apimachinery/pkg/util/sets"
	cliflag "k8s.io/component-base/cli/flag"
)

const (
	usageFmt = "Usage:\n  %s\n"
)

// setPartialUsageAndHelpFunc set both usage and help function.
// Print the flag sets we need instead of all of them.
func setPartialUsageAndHelpFunc(cmd *cobra.Command, fss cliflag.NamedFlagSets, cols int, flags []string) {
	cmd.SetUsageFunc(func(cmd *cobra.Command) error {
		fmt.Fprintf(cmd.OutOrStderr(), usageFmt, cmd.UseLine())
		printMostImportantFlags(cmd.OutOrStderr(), fss, cols, flags)
		fmt.Fprintf(cmd.OutOrStderr(), "\nUse \"%s\" for a list of all flags available.\n", cmd.CommandPath())
		return nil
	})
	cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n\n"+usageFmt, cmd.Long, cmd.UseLine())
		printMostImportantFlags(cmd.OutOrStdout(), fss, cols, flags)
		fmt.Fprintf(cmd.OutOrStderr(), "\nUse \"%s options\" for a list of all flags available.\n", cmd.CommandPath())
	})
}

func printMostImportantFlags(w io.Writer, fss cliflag.NamedFlagSets, cols int, visibleFlags []string) {
	visibleFlagsSet := sets.New[string](visibleFlags...)
	filteredFFS := cliflag.NamedFlagSets{}
	filteredFS := filteredFFS.FlagSet("Most important")

	for _, name := range fss.Order {
		fs := fss.FlagSets[name]
		if !fs.HasFlags() {
			continue
		}

		fs.VisitAll(func(f *pflag.Flag) {
			if visibleFlagsSet.Has(f.Name) {
				filteredFS.AddFlag(f)
			}
		})
	}

	cliflag.PrintSections(w, filteredFFS, cols)
}

func parseControllerSettings(flagString string) map[string]int {
	settingsMap := make(map[string]int)

	// Split the flag string based on commas
	settings := strings.Split(flagString, ",")

	// Iterate over each setting
	for _, setting := range settings {
		// Split the setting into key and value based on '='
		parts := strings.Split(setting, "=")
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			num, err := strconv.Atoi(value)
			if err != nil {
				fmt.Println("Error:", err)
				continue
			}
			settingsMap[key] = num
		} else {
			// Handle invalid format
			fmt.Printf("Invalid format for setting: %s\n", setting)
		}
	}

	return settingsMap
}
