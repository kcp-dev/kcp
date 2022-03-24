/*
Copyright 2021 The KCP Authors.

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

package apiimporter

import (
	"github.com/spf13/pflag"
)

func DefaultOptions() *Options {
	return &Options{
		ResourcesToSync: []string{"deployments.apps"},
	}
}

func BindOptions(o *Options, fs *pflag.FlagSet) *Options {
	fs.StringSliceVar(&o.ResourcesToSync, "resources-to-sync", o.ResourcesToSync, "Provides the list of resources that should be synced from KCP logical cluster to underlying physical clusters")
	return o
}

type Options struct {
	ResourcesToSync []string
}

func (o *Options) Validate() error {
	return nil
}
