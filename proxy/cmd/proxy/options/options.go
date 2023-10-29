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

package options

import (
	"errors"

	"github.com/spf13/pflag"

	"k8s.io/component-base/logs"
	logsapiv1 "k8s.io/component-base/logs/api/v1"
)

type Options struct {
	QPS             float32
	Burst           int
	FromKubeconfig  string
	FromContext     string
	FromClusterPath string
	ToKubeconfig    string
	ToContext       string
	ProxyTargetName string
	ProxyTargetUID  string
	Logs            *logs.Options
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
	fs.StringVar(&options.FromKubeconfig, "from-kubeconfig", options.FromKubeconfig, "Kubeconfig file for -from cluster.")
	fs.StringVar(&options.FromContext, "from-context", options.FromContext, "Context to use in the Kubeconfig file for -from cluster, instead of the current context.")
	fs.StringVar(&options.FromClusterPath, "from-cluster", options.FromClusterPath, "Path of the -from logical cluster.")
	fs.StringVar(&options.ToKubeconfig, "to-kubeconfig", options.ToKubeconfig, "Kubeconfig file for -to cluster. If not set, the InCluster configuration will be used.")
	fs.StringVar(&options.ToContext, "to-context", options.ToContext, "Context to use in the Kubeconfig file for -to cluster, instead of the current context.")
	fs.StringVar(&options.ProxyTargetName, "proxy-target-name", options.ProxyTargetName, "The name from the Proxy target resource in KCP.")
	fs.StringVar(&options.ProxyTargetUID, "proxy-target-uid", options.ProxyTargetUID, "The UID from the Proxy target resource in KCP.")

	logsapiv1.AddFlags(options.Logs, fs)
}

func (options *Options) Complete() error {
	return nil
}

func (options *Options) Validate() error {
	if options.FromClusterPath == "" {
		return errors.New("--from-cluster is required")
	}
	if options.FromKubeconfig == "" {
		return errors.New("--from-kubeconfig is required")
	}
	if options.ProxyTargetUID == "" {
		return errors.New("--proxy-target-uid is required")
	}
	return nil
}
