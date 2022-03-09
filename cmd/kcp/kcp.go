/*
Copyright 2021 The KCP Authors.

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
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"

	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/util/errors"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/cli/globalflag"
	"k8s.io/component-base/term"

	"github.com/kcp-dev/kcp/pkg/cmd/help"
	"github.com/kcp-dev/kcp/pkg/server"
	"github.com/kcp-dev/kcp/pkg/server/options"
)

func main() {
	help.FitTerminal()

	// cli flags for profiling
	var (
		outputDir            string
		memProfile           string
		memProfileRate       int
		cpuProfile           string
		blockProfile         string
		blockProfileRate     int
		mutexProfile         string
		mutexProfileFraction int
		traceFile            string
	)

	cmd := &cobra.Command{
		Use:   "kcp",
		Short: "Kube for Control Plane (KCP)",
		Long: help.Doc(`
			KCP is the easiest way to manage Kubernetes applications against one or
			more clusters, by giving you a personal control plane that schedules your
			workloads onto one or many clusters, and making it simple to pick up and
			move. Advanced use cases including spreading your apps across clusters for
			resiliency, scheduling batch workloads onto clusters with free capacity,
			and enabling collaboration for individual teams without having access to
			the underlying clusters.

			To get started, launch a new cluster with 'kcp start', which will
			initialize your personal control plane and write an admin kubeconfig file
			to disk.
		`),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cols, _, _ := term.TerminalSize(cmd.OutOrStdout())

	serverOptions := options.NewOptions()
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the control plane process",
		Long: help.Doc(`
			Start the control plane process

			The server process listens on port 6443 and will act like a Kubernetes
			API server. It will initialize any necessary data to the provided start
			location or as a '.kcp' directory in the current directory. An admin
			kubeconfig file will be generated at initialization time that may be
			used to access the control plane.
		`),
		PersistentPreRunE: func(*cobra.Command, []string) error {
			// silence client-go warnings.
			// apiserver loopback clients should not log self-issued warnings.
			rest.SetDefaultWarningHandler(rest.NoWarnings{})
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Activate logging as soon as possible.
			if err := serverOptions.GenericControlPlane.Logs.ValidateAndApply(); err != nil {
				return err
			}

			completed, err := serverOptions.Complete()
			if err != nil {
				return err
			}

			if errs := completed.Validate(); len(errs) > 0 {
				return errors.NewAggregate(errs)
			}

			s, err := server.NewServer(completed)
			if err != nil {
				return err
			}

			// cli profiling
			if memProfileRate > 0 {
				runtime.MemProfileRate = memProfileRate
			}
			if blockProfile != "" && blockProfileRate >= 0 {
				runtime.SetBlockProfileRate(blockProfileRate)
			}
			if mutexProfile != "" && mutexProfileFraction >= 0 {
				runtime.SetMutexProfileFraction(mutexProfileFraction)
			}

			if cpuProfile != "" {
				f, err := os.Create(filepath.Join(outputDir, cpuProfile))
				if err != nil {
					fmt.Fprintf(os.Stderr, "kcp: %s\n", err)
					return err
				}
				if err := pprof.StartCPUProfile(f); err != nil {
					fmt.Fprintf(os.Stderr, "kcp: can't start cpu profile: %s\n", err)
					f.Close()
					return err
				}
				defer pprof.StopCPUProfile()
			}

			if memProfile != "" {
				f, err := os.Create(filepath.Join(outputDir, memProfile))
				if err != nil {
					fmt.Fprintf(os.Stderr, "kcp: %s\n", err)
					return err
				}
				defer func() {
					pprof.Lookup("heap").WriteTo(f, 0)
					f.Close()
				}()
			}

			if mutexProfile != "" {
				f, err := os.Create(filepath.Join(outputDir, mutexProfile))
				if err != nil {
					fmt.Fprintf(os.Stderr, "kcp: %s\n", err)
					return err
				}
				defer func() {
					pprof.Lookup("mutex").WriteTo(f, 0)
					f.Close()
				}()
			}

			if blockProfile != "" {
				f, err := os.Create(filepath.Join(outputDir, blockProfile))
				if err != nil {
					fmt.Fprintf(os.Stderr, "kcp: %s\n", err)
					return err
				}
				defer func() {
					pprof.Lookup("block").WriteTo(f, 0)
					f.Close()
				}()
			}

			if traceFile != "" {
				f, err := os.Create(filepath.Join(outputDir, traceFile))
				if err != nil {
					fmt.Fprintf(os.Stderr, "kcp: %s\n", err)
					return err
				}
				if err := trace.Start(f); err != nil {
					fmt.Fprintf(os.Stderr, "kcp: can't start tracing: %s\n", err)
					f.Close()
					return err
				}
				defer trace.Stop()
			}

			return s.Run(genericapiserver.SetupSignalContext())
		},
	}

	startCmd.Flags().StringVarP(&outputDir, "profile-outputdir", "", "", "write profiles to `dir`")
	startCmd.Flags().StringVarP(&memProfile, "memprofile", "", "", "write an allocation profile to `file`")
	startCmd.Flags().IntVarP(&memProfileRate, "memprofilerate", "", 0, "set memory allocation profiling `rate` (see runtime.MemProfileRate)")
	startCmd.Flags().StringVarP(&cpuProfile, "cpuprofile", "", "", "write a cpu profile to `file`")
	startCmd.Flags().StringVarP(&blockProfile, "blockprofile", "", "", "write a goroutine blocking profile to `file`")
	startCmd.Flags().IntVarP(&blockProfileRate, "blockprofilerate", "", 1, "set blocking profile `rate` (see runtime.SetBlockProfileRate)")
	startCmd.Flags().StringVarP(&mutexProfile, "mutexprofile", "", "", "write a mutex contention profile to the named file after execution")
	startCmd.Flags().IntVarP(&mutexProfileFraction, "mutexprofilefraction", "", 1, "if >= 0, calls runtime.SetMutexProfileFraction()")
	startCmd.Flags().StringVarP(&traceFile, "trace", "", "", "write an execution trace to `file`")

	// add start named flag sets to start flags
	namedStartFlagSets := serverOptions.Flags()
	globalflag.AddGlobalFlags(namedStartFlagSets.FlagSet("global"), cmd.Name())
	startFlags := startCmd.Flags()
	for _, f := range namedStartFlagSets.FlagSets {
		startFlags.AddFlagSet(f)
	}

	startOptionsCmd := &cobra.Command{
		Use:   "options",
		Short: "Show all start command options",
		Long: help.Doc(`
			Show all start command options

			"kcp start"" has a large number of options. This command shows all of them.
		`),
		PersistentPreRunE: func(*cobra.Command, []string) error {
			// silence client-go warnings.
			// apiserver loopback clients should not log self-issued warnings.
			rest.SetDefaultWarningHandler(rest.NoWarnings{})
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStderr(), usageFmt, startCmd.UseLine())
			cliflag.PrintSections(cmd.OutOrStderr(), namedStartFlagSets, cols)
			return nil
		},
	}
	startCmd.AddCommand(startOptionsCmd)
	cmd.AddCommand(startCmd)

	setPartialUsageAndHelpFunc(cmd, namedStartFlagSets, cols, []string{
		"etcd-servers",
		"run-controllers",
	})

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
}
