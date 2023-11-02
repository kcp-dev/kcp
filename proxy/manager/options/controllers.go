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

	proxy "github.com/kcp-dev/kcp/proxy/reconciler/proxy/options"
)

type Controllers struct {
	WorkspaceProxy WorkspaceProxyController
}

type WorkspaceProxyController = proxy.Options

func NewProxyControllers() *Controllers {
	return &Controllers{
		WorkspaceProxy: *proxy.NewOptions(),
	}
}

func (c *Controllers) AddFlags(fs *pflag.FlagSet) {
	c.WorkspaceProxy.AddFlags(fs)
}

func (c *Controllers) Complete() error {
	return nil
}

func (c *Controllers) Validate() []error {
	var errs []error

	if err := c.WorkspaceProxy.Validate(); err != nil {
		errs = append(errs, err)
	}

	return errs
}
