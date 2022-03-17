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
	"fmt"

	"github.com/spf13/pflag"
)

type Options struct {
	MappingFile string
}

func NewOptions() *Options {
	o := &Options{}
	return o
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.MappingFile, "mapping-file", o.MappingFile, "Config file mapping paths to backends")
}

func (o *Options) Complete() error {
	return nil
}

func (o *Options) Validate() []error {
	var errs []error

	if o.MappingFile == "" {
		errs = append(errs, fmt.Errorf("--mapping-file is required"))
	}

	return errs
}
