/*
Copyright 2023 The KCP Authors.

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

package options

import (
	"errors"
	"os"

	"github.com/spf13/pflag"

	"k8s.io/component-base/logs"
	logsapiv1 "k8s.io/component-base/logs/api/v1"
)

type Options struct {
	QPS        float32
	Burst      int
	Kubeconfig string
	Context    string
	Logs       *logs.Options
}

func NewOptions() *Options {
	// Default to -v=2
	logsOptions := logs.NewOptions()
	logsOptions.Verbosity = logsapiv1.VerbosityLevel(2)

	return &Options{
		QPS:   30,
		Burst: 20,
		Logs:  logsOptions,
	}
}

func (options *Options) AddFlags(fs *pflag.FlagSet) {
	fs.Float32Var(&options.QPS, "qps", options.QPS, "QPS to use when talking to API servers.")
	fs.IntVar(&options.Burst, "burst", options.Burst, "Burst to use when talking to API servers.")
	fs.StringVar(&options.Kubeconfig, "kubeconfig", options.Kubeconfig, "Kubeconfig file for the kcp workspace.")
	fs.StringVar(&options.Context, "context", options.Context, "Context to use in the kubeconfig file for the kcp workspace, instead of the current context.")
	logsapiv1.AddFlags(options.Logs, fs)
}

func (options *Options) Complete() error {
	if options.Kubeconfig == "" {
		if kubeconfig := os.Getenv("KUBECONFIG"); kubeconfig != "" {
			options.Kubeconfig = kubeconfig
		}
	}
	return nil
}

func (options *Options) Validate() error {
	if options.Kubeconfig == "" {
		return errors.New("--from-kubeconfig is required")
	}
	return nil
}
