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
	"net/url"

	"github.com/spf13/pflag"

	genericoptions "k8s.io/apiserver/pkg/server/options"

	cacheoptions "github.com/kcp-dev/kcp/pkg/cache/server/options"
)

type cacheCompleted struct {
	Server *cacheoptions.CompletedOptions
	Extra
}

func (c cacheCompleted) Validate() []error {
	var errs []error
	if _, err := url.Parse(c.URL); err != nil {
		errs = append(errs, err)
	}
	if err := c.Server.Validate(); err != nil {
		errs = append(errs, err...)
	}
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

	// URL the url address of the cache server
	URL string
}

func NewCache(rootDir string) *Cache {
	return &Cache{
		Server: cacheoptions.NewOptions(rootDir),
	}
}

func (c *Cache) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&c.URL, "cache-url", c.URL, "A URL address of a cache server associated with this instance (default https://localhost:6443)")
	fs.BoolVar(&c.Enabled, "run-cache-server", c.Enabled, "If set to true it runs the cache server with this instance (default false)")

	c.Server.AddFlags(fs)
}

func (c *Cache) Complete(secureServing *genericoptions.SecureServingOptionsWithLoopback) (cacheCompleted, error) {
	if len(c.URL) == 0 {
		bindPort := 6443
		if secureServing != nil && secureServing.BindPort != bindPort {
			bindPort = secureServing.BindPort
		}
		c.URL = fmt.Sprintf("https://localhost:%v", bindPort)
	}
	serverCompletedOptions, err := c.Server.Complete()
	if err != nil {
		return cacheCompleted{}, err
	}

	return cacheCompleted{serverCompletedOptions, c.Extra}, nil
}
