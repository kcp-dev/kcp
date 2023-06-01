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
	cliflag "k8s.io/component-base/cli/flag"

	kcpcoreoptions "github.com/kcp-dev/kcp/pkg/server/options"
)

type Options struct {
	Core kcpcoreoptions.Options

	Extra ExtraOptions
}

type ExtraOptions struct {
}

type completedOptions struct {
	Core kcpcoreoptions.CompletedOptions

	Extra ExtraOptions
}

type CompletedOptions struct {
	*completedOptions
}

// NewOptions creates a new Options with default parameters.
func NewOptions(rootDir string) *Options {
	o := &Options{
		Core: *kcpcoreoptions.NewOptions(rootDir),

		Extra: ExtraOptions{},
	}

	return o
}

func (o *Options) AddFlags(fss *cliflag.NamedFlagSets) {
	o.Core.AddFlags(fss)
}

func (o *CompletedOptions) Validate() []error {
	return o.Core.Validate()
}

func (o *Options) Complete(rootDir string) (*CompletedOptions, error) {
	core, err := o.Core.Complete(rootDir)
	if err != nil {
		return nil, err
	}

	return &CompletedOptions{
		completedOptions: &completedOptions{
			Core:  *core,
			Extra: o.Extra,
		},
	}, nil
}
