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

	"github.com/kcp-dev/kcp/pkg/reconciler/cluster"
)

type Controllers struct {
	EnableAll           bool
	IndividuallyEnabled []string
	Cluster             ClusterController
}

type ClusterController = cluster.Options

func NewControllers() *Controllers {
	return &Controllers{
		EnableAll: true,

		Cluster: *cluster.DefaultOptions(),
	}
}

func (c *Controllers) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&c.EnableAll, "run-controllers", c.EnableAll, "Run the controllers in-process")

	fs.StringSliceVar(&c.IndividuallyEnabled, "unsupported-run-individual-controllers", c.IndividuallyEnabled, "Run individual controllers in-process. The controller names can change at any time.")
	fs.MarkHidden("unsupported-run-individual-controllers") //nolint:errcheck

	cluster.BindOptions(&c.Cluster, fs)
}

func (c *Controllers) Validate() []error {
	var errs []error

	if err := c.Cluster.Validate(); err != nil {
		errs = append(errs, err)
	}

	return errs
}
