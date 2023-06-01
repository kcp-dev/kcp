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
	"io"

	cliflag "k8s.io/component-base/cli/flag"

	kcpcoreoptions "github.com/kcp-dev/kcp/cmd/kcp-core/options"
	serveroptions "github.com/kcp-dev/kcp/pkg/server/options"
)

type Options struct {
	Output io.Writer

	Generic kcpcoreoptions.GenericOptions
	Server  serveroptions.Options
	Extra   ExtraOptions
}

type ExtraOptions struct{}

func NewOptions(rootDir string) *Options {
	opts := &Options{
		Output: nil,

		Server:  *serveroptions.NewOptions(rootDir),
		Generic: *kcpcoreoptions.NewGeneric(rootDir),
		Extra:   ExtraOptions{},
	}

	return opts
}

type completedOptions struct {
	Output io.Writer

	Generic kcpcoreoptions.GenericOptions
	Server  serveroptions.CompletedOptions
	Extra   ExtraOptions
}

type CompletedOptions struct {
	*completedOptions
}

func (o *Options) AddFlags(fss *cliflag.NamedFlagSets) {
	o.Generic.AddFlags(fss)
	o.Server.AddFlags(fss)
}

func (o *Options) Complete() (*CompletedOptions, error) {
	generic, err := o.Generic.Complete()
	if err != nil {
		return nil, err
	}

	server, err := o.Server.Complete(generic.RootDirectory)
	if err != nil {
		return nil, err
	}

	return &CompletedOptions{
		completedOptions: &completedOptions{
			Output:  o.Output,
			Generic: *generic,
			Server:  *server,
		},
	}, nil
}

func (o *CompletedOptions) Validate() []error {
	errs := []error{}

	errs = append(errs, o.Generic.Validate()...)
	errs = append(errs, o.Server.Validate()...)

	return errs
}
