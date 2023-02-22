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

	apiresource "github.com/kcp-dev/kcp/pkg/reconciler/apis/apiresource/options"
	heartbeat "github.com/kcp-dev/kcp/pkg/reconciler/workload/heartbeat/options"
)

type Controllers struct {
	ApiResource         ApiResourceController
	SyncTargetHeartbeat SyncTargetHeartbeatController
}

type ApiResourceController = apiresource.Options
type SyncTargetHeartbeatController = heartbeat.Options

func NewTmcControllers() *Controllers {
	return &Controllers{
		ApiResource:         *apiresource.NewOptions(),
		SyncTargetHeartbeat: *heartbeat.NewOptions(),
	}
}

func (c *Controllers) AddFlags(fs *pflag.FlagSet) {
	c.SyncTargetHeartbeat.AddFlags(fs)
	c.ApiResource.AddFlags(fs)
}

func (c *Controllers) Complete(rootDir string) error {
	return nil
}

func (c *Controllers) Validate() []error {
	var errs []error

	if err := c.ApiResource.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := c.SyncTargetHeartbeat.Validate(); err != nil {
		errs = append(errs, err)
	}

	return errs
}
