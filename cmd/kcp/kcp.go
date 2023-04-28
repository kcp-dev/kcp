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
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/util/errors"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/component-base/cli"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/cli/globalflag"
	"k8s.io/component-base/logs"
	logsapiv1 "k8s.io/component-base/logs/api/v1"
	_ "k8s.io/component-base/logs/json/register"
	"k8s.io/component-base/term"
	"k8s.io/component-base/version"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/cmd/kcp/options"
	"github.com/kcp-dev/kcp/pkg/cmd/help"
	"github.com/kcp-dev/kcp/pkg/embeddedetcd"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	tmcserver "github.com/kcp-dev/kcp/tmc/pkg/server"
)

func main() {
	rand.Seed(time.Now().UTC().UnixNano())

	cmd := &cobra.Command{
		Use:   "kcp",
		Short: "Kube for Control Plane (KCP)",
		Long: help.Doc(`
			KCP is the easiest way to manage Kubernetes applications against one or
			more clusters, by giving you a personal control plane that schedules your
			workloads onto one or many clusters, and making it simple to pick up and
			move. It supports advanced use cases such as spreading your apps across
			clusters for resiliency, scheduling batch workloads onto clusters with
			free capacity, and enabling collaboration for individual teams without
			having access to the underlying clusters.

			To get started, launch a new cluster with 'kcp start', which will
			initialize your personal control plane and write an admin kubeconfig file
			to disk.
		`),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cols, _, _ := term.TerminalSize(cmd.OutOrStdout())

	// manually extract root directory from flags first as it influence all other flags
	rootDir := ".kcp"
	for i, f := range os.Args {
		if f == "--root-directory" {
			if i < len(os.Args)-1 {
				rootDir = os.Args[i+1]
			} // else let normal flag processing fail
		} else if strings.HasPrefix(f, "--root-directory=") {
			rootDir = strings.TrimPrefix(f, "--root-directory=")
		}
	}

	serverOptions := options.NewOptions(rootDir)
	serverOptions.Server.Core.GenericControlPlane.Logs.Verbosity = logsapiv1.VerbosityLevel(2)

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
			// run as early as possible to avoid races later when some components (e.g. grpc) start early using klog
			if err := logsapiv1.ValidateAndApply(serverOptions.Server.Core.GenericControlPlane.Logs, kcpfeatures.DefaultFeatureGate); err != nil {
				return err
			}

			completed, err := serverOptions.Complete()
			if err != nil {
				return err
			}

			if errs := completed.Validate(); len(errs) > 0 {
				return errors.NewAggregate(errs)
			}

			logger := klog.FromContext(cmd.Context())
			logger.Info("running with selected batteries", "batteries", strings.Join(completed.Server.Core.Extra.BatteriesIncluded, ","))

			config, err := tmcserver.NewConfig(completed.Server)
			if err != nil {
				return err
			}

			completedConfig, err := config.Complete()
			if err != nil {
				return err
			}

			ctx := genericapiserver.SetupSignalContext()

			// the etcd server must be up before NewServer because storage decorators access it right away
			if completedConfig.Core.EmbeddedEtcd.Config != nil {
				if err := embeddedetcd.NewServer(completedConfig.Core.EmbeddedEtcd).Run(ctx); err != nil {
					return err
				}
			}

			s, err := tmcserver.NewServer(completedConfig)
			if err != nil {
				return err
			}

			pprofDir := os.Getenv("PPROFDIR")
			if pprofDir != "" {
				cpuFilePath := filepath.Join(pprofDir, "cpu.pprof")
				cpu, err := os.Create(cpuFilePath)
				if err != nil {
					return fmt.Errorf("error creating cpu profiling file %q: %w", cpuFilePath, err)
				}
				pprof.StartCPUProfile(cpu)

				memFilePath := filepath.Join(pprofDir, "mem.pprof")
				mem, err := os.Create(memFilePath)
				if err != nil {
					return fmt.Errorf("error creating mem profiling file %q: %w", memFilePath, err)
				}

				s.Core.AddPreShutdownHook("pprof", func() error {
					logger.Info("Stopping CPU profile")
					pprof.StopCPUProfile()
					cpu.Close()

					logger.Info("Performing GC")
					runtime.GC()

					logger.Info("Writing heap profile")
					pprof.WriteHeapProfile(mem)

					logger.Info("Closing mem.pprof")
					mem.Close()

					return nil
				})
			}

			err = s.Run(ctx)
			<-ctx.Done()
			return err
		},
	}

	// add start named flag sets to start flags
	fss := cliflag.NamedFlagSets{}
	serverOptions.AddFlags(&fss)
	globalflag.AddGlobalFlags(fss.FlagSet("global"), cmd.Name(), logs.SkipLoggingConfigurationFlags())
	startFlags := startCmd.Flags()
	for _, f := range fss.FlagSets {
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
			cliflag.PrintSections(cmd.OutOrStderr(), fss, cols)
			return nil
		},
	}
	startCmd.AddCommand(startOptionsCmd)
	cmd.AddCommand(startCmd)

	setPartialUsageAndHelpFunc(startCmd, fss, cols, []string{
		"etcd-servers",
		"batteries-included",
		"run-virtual-workspaces",
	})

	help.FitTerminal(cmd.OutOrStdout())

	if v := version.Get().String(); len(v) == 0 {
		cmd.Version = "<unknown>"
	} else {
		cmd.Version = v
	}

	os.Exit(cli.Run(cmd))
}
