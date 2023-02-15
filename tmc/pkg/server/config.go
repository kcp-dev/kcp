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

package server

import (
	_ "net/http/pprof"

	coreserver "github.com/kcp-dev/kcp/pkg/server"
	"github.com/kcp-dev/kcp/tmc/pkg/server/options"
)

type Config struct {
	Options options.CompletedOptions

	Core *coreserver.Config

	ExtraConfig
}

type ExtraConfig struct {
}

type completedConfig struct {
	Options options.CompletedOptions
	Core    coreserver.CompletedConfig

	ExtraConfig
}

type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *Config) Complete() (CompletedConfig, error) {
	core, err := c.Core.Complete()
	if err != nil {
		return CompletedConfig{}, err
	}

	return CompletedConfig{&completedConfig{
		Options: c.Options,
		Core:    core,

		ExtraConfig: c.ExtraConfig,
	}}, nil
}

func NewConfig(opts options.CompletedOptions) (*Config, error) {
	core, err := coreserver.NewConfig(opts.Core)
	if err != nil {
		return nil, err
	}

	c := &Config{
		Options:     opts,
		Core:        core,
		ExtraConfig: ExtraConfig{},
	}

	return c, nil
}
