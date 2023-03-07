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
	"runtime"

	"github.com/spf13/pflag"
)

// NewOptions are the default options for the apiresource controller.
func NewOptions() *Options {
	return &Options{
		// Consumed by server instantiation
		NumThreads: runtime.NumCPU(),
	}
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	if o == nil {
		return
	}

	fs.IntVar(&o.NumThreads, "apiresource-controller-threads", o.NumThreads, "Number of threads to use for the apiresource controller.")
}

// Options are the options for the cluster controller.
type Options struct {
	NumThreads int
}

func (o *Options) Validate() error {
	if o == nil {
		return nil
	}

	return nil
}
