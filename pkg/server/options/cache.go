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
	"github.com/spf13/pflag"

	cacheclientoptions "github.com/kcp-dev/kcp/pkg/cache/client/options"
	cacheoptions "github.com/kcp-dev/kcp/pkg/cache/server/options"
)

type cacheCompleted struct {
	Server *cacheoptions.CompletedOptions
	Extra
}

func (c cacheCompleted) Validate() []error {
	var errs []error

	if err := c.Server.Validate(); err != nil {
		errs = append(errs, err...)
	}
	errs = append(errs, c.Extra.Client.Validate()...)

	return errs
}

type Cache struct {
	// Server includes options provided by the cache server
	Server *cacheoptions.Options
	Extra
}
type Extra struct {
	// Enabled if true indicates that the cache server should be run with the kcp-server (in-process)
	Enabled bool

	Client cacheclientoptions.Cache
}

func NewCache(rootDir string) *Cache {
	return &Cache{
		Server: cacheoptions.NewOptions(rootDir),
		Extra: Extra{
			Client: *cacheclientoptions.NewCache(),
		},
	}
}

func (c *Cache) AddFlags(fs *pflag.FlagSet) {
	c.Client.AddFlags(fs)

	// note do not add cache server's flag c.Server.AddFlags(fs)
	// it will cause an undefined behavior as some flags will be overwritten (also defined by the kcp server)
	// as of today all required flags (embedded etcd, secure port)) are provided by the kcp server, so we are fine for now
	// it will be finally addressed in https://github.com/kcp-dev/kcp/issues/2021
}

func (c *Cache) Complete() (cacheCompleted, error) {
	serverCompletedOptions, err := c.Server.Complete()
	if err != nil {
		return cacheCompleted{}, err
	}

	return cacheCompleted{serverCompletedOptions, c.Extra}, nil
}
