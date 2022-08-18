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
	cacheserveroptions "github.com/kcp-dev/kcp/pkg/cache/server/options"
)

type Config struct {
	Options *cacheserveroptions.CompletedOptions
}

type completedConfig struct {
	Options *cacheserveroptions.CompletedOptions
}

type CompletedConfig struct {
	// embed a private pointer that cannot be instantiated outside this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *Config) Complete() (CompletedConfig, error) {
	return CompletedConfig{&completedConfig{
		Options: c.Options,
	}}, nil
}

func NewConfig(opts *cacheserveroptions.CompletedOptions) (*Config, error) {
	c := &Config{
		Options: opts,
	}
	return c, nil
}
