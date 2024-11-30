/*
Copyright 2022 The Kube Bind Authors.

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
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"

	genericapiserver "k8s.io/apiserver/pkg/server"
	logsv1 "k8s.io/component-base/logs/api/v1"
	"k8s.io/component-base/version"
	"k8s.io/klog/v2"

	backend "github.com/kcp-dev/kcp/contrib/kube-bind"
	"github.com/kcp-dev/kcp/contrib/kube-bind/options"
)

func main() {
	ctx := genericapiserver.SetupSignalContext()
	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v", err) // nolint: errcheck
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	defer klog.Flush()

	options := options.NewOptions()
	options.AddFlags(pflag.CommandLine)
	pflag.Parse()

	// setup logging first
	if err := logsv1.ValidateAndApply(options.Logs, nil); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v", err) // nolint: errcheck
		os.Exit(1)
	}
	ver := version.Get().GitVersion
	if i := strings.Index(ver, "bind-"); i != -1 {
		ver = ver[i+5:] // example: v1.25.2+kubectl-bind-v0.0.7-52-g8fee0baeaff3aa
	}
	logger := klog.FromContext(ctx)
	logger.Info("Starting example-backend", "version", ver)

	// create server
	completed, err := options.Complete()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v", err) // nolint: errcheck
		os.Exit(1)
	}
	if err := completed.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v", err) // nolint: errcheck
		os.Exit(1)
	}

	// start server
	config, err := backend.NewConfig(completed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v", err) // nolint: errcheck
		os.Exit(1)
	}
	server, err := backend.NewServer(ctx, config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v", err) // nolint: errcheck
		os.Exit(1)
	}
	server.OptionallyStartInformers(ctx)
	if err := server.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v", err) // nolint: errcheck
		os.Exit(1)
	}
	fmt.Printf("Listening on port %s\n", server.Addr())

	<-ctx.Done()
	return nil
}
