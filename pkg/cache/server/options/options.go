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

import "github.com/spf13/pflag"

type Options struct{}

type completedOptions struct{}

type CompletedOptions struct {
	*completedOptions
}

func (o *CompletedOptions) Validate() []error { return nil }

// NewOptions creates a new Options with default parameters.
func NewOptions() *Options { return &Options{} }

func (o *Options) Complete() (*CompletedOptions, error) {
	return &CompletedOptions{&completedOptions{}}, nil
}

func (o *Options) AddFlags(flags *pflag.FlagSet) {}
