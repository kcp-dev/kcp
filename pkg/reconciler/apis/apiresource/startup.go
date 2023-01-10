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

package apiresource

import (
	"runtime"

	"github.com/spf13/pflag"

	"k8s.io/klog/v2"
)

// DefaultOptions are the default options for the apiresource controller.
func DefaultOptions() *Options {
	return &Options{
		// Consumed by server instantiation
		NumThreads: runtime.NumCPU(),
	}
}

// BindOptions binds the apiresource controller options to the flag set.
func BindOptions(o *Options, fs *pflag.FlagSet) *Options {
	fs.BoolVar(&o.AutoPublishAPIs, "auto-publish-apis", o.AutoPublishAPIs, "If true, the APIs imported from physical clusters will be published automatically as CRDs")
	fs.MarkDeprecated("auto-publish-apis", "This flag is deprecated and ignored. It will be removed in a future release.") //nolint:errcheck
	fs.IntVar(&o.NumThreads, "apiresource-controller-threads", o.NumThreads, "Number of threads to use for the cluster controller.")
	return o
}

// Options are the options for the cluster controller.
type Options struct {
	AutoPublishAPIs bool
	NumThreads      int
}

func (o *Options) Validate() error {
	if o.AutoPublishAPIs {
		klog.Background().Info("--auto-publish-apis is deprecated and ignored. Please remove it from the command line.")
		o.AutoPublishAPIs = false
	}

	return nil
}
