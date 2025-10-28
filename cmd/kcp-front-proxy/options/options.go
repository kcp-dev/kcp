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
	"strings"

	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/component-base/logs"
	logsapiv1 "k8s.io/component-base/logs/api/v1"

	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	proxyoptions "github.com/kcp-dev/kcp/pkg/proxy/options"
)

type Options struct {
	Proxy *proxyoptions.Options
	Logs  *logs.Options
}

func NewOptions() *Options {
	o := &Options{
		Proxy: proxyoptions.NewOptions(),
		Logs:  logs.NewOptions(),
	}

	// Default to -v=2
	o.Logs.Verbosity = logsapiv1.VerbosityLevel(2)
	return o
}

func (o *Options) AddFlags(fss *cliflag.NamedFlagSets) {
	o.Proxy.AddFlags(fss)
	logsapiv1.AddFlags(o.Logs, fss.FlagSet("logging"))

	// add flags that are filtered out from upstream, but overridden here with our own version
	fss.FlagSet("kcp").Var(kcpfeatures.NewFlagValue(), "feature-gates", ""+
		"A set of key=value pairs that describe feature gates for alpha/experimental features. "+
		"Options are:\n"+strings.Join(kcpfeatures.KnownFeatures(), "\n")) // hide kube-only gates
}

func (o *Options) Complete() error {
	return o.Proxy.Complete()
}

func (o *Options) Validate() []error {
	var errs []error

	errs = append(errs, o.Proxy.Validate()...)

	return errs
}
