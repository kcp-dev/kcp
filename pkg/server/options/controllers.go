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

	"k8s.io/klog/v2"
	kcmoptions "k8s.io/kubernetes/cmd/kube-controller-manager/app/options"

	"github.com/kcp-dev/kcp/pkg/reconciler/apiresource"
	"github.com/kcp-dev/kcp/pkg/reconciler/cluster/apiimporter"
	"github.com/kcp-dev/kcp/pkg/reconciler/cluster/syncer"
)

type Controllers struct {
	EnableAll           bool
	IndividuallyEnabled []string
	ApiImporter         ApiImporterController
	ApiResource         ApiResourceController
	Syncer              SyncerController
	SAController        kcmoptions.SAControllerOptions
}

type ApiImporterController = apiimporter.Options
type ApiResourceController = apiresource.Options
type SyncerController = syncer.Options

var kcmDefaults *kcmoptions.KubeControllerManagerOptions

func init() {
	var err error

	kcmDefaults, err = kcmoptions.NewKubeControllerManagerOptions()
	if err != nil {
		klog.Fatal(err)
	}
}

func NewControllers() *Controllers {
	return &Controllers{
		EnableAll: true,

		ApiImporter:  *apiimporter.DefaultOptions(),
		ApiResource:  *apiresource.DefaultOptions(),
		Syncer:       *syncer.DefaultOptions(),
		SAController: *kcmDefaults.SAController,
	}
}

func (c *Controllers) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&c.EnableAll, "run-controllers", c.EnableAll, "Run the controllers in-process")

	fs.StringSliceVar(&c.IndividuallyEnabled, "unsupported-run-individual-controllers", c.IndividuallyEnabled, "Run individual controllers in-process. The controller names can change at any time.")
	fs.MarkHidden("unsupported-run-individual-controllers") //nolint:errcheck

	apiimporter.BindOptions(&c.ApiImporter, fs)
	apiresource.BindOptions(&c.ApiResource, fs)
	syncer.BindOptions(&c.Syncer, fs)

	c.SAController.AddFlags(fs)
}

func (c *Controllers) Validate() []error {
	var errs []error

	if err := c.ApiImporter.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := c.ApiResource.Validate(); err != nil {
		errs = append(errs, err)
	}
	if err := c.Syncer.Validate(); err != nil {
		errs = append(errs, err)
	}
	if saErrs := c.SAController.Validate(); saErrs != nil {
		errs = append(errs, saErrs...)
	}

	return errs
}
