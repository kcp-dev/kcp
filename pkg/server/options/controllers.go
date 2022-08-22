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
	cryptorand "crypto/rand"
	"crypto/rsa"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/pflag"

	"k8s.io/client-go/util/keyutil"
	"k8s.io/klog/v2"
	kcmoptions "k8s.io/kubernetes/cmd/kube-controller-manager/app/options"

	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apiresource"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/heartbeat"
)

type Controllers struct {
	EnableAll           bool
	IndividuallyEnabled []string
	ApiResource         ApiResourceController
	SyncTargetHeartbeat SyncTargetHeartbeatController
	SAController        kcmoptions.SAControllerOptions
}

type ApiResourceController = apiresource.Options
type SyncTargetHeartbeatController = heartbeat.Options

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

		ApiResource:         *apiresource.DefaultOptions(),
		SyncTargetHeartbeat: *heartbeat.DefaultOptions(),
		SAController:        *kcmDefaults.SAController,
	}
}

func (c *Controllers) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&c.EnableAll, "run-controllers", c.EnableAll, "Run the controllers in-process")

	fs.StringSliceVar(&c.IndividuallyEnabled, "unsupported-run-individual-controllers", c.IndividuallyEnabled, "Run individual controllers in-process. The controller names can change at any time.")
	fs.MarkHidden("unsupported-run-individual-controllers") //nolint:errcheck

	apiresource.BindOptions(&c.ApiResource, fs)
	heartbeat.BindOptions(&c.SyncTargetHeartbeat, fs)

	c.SAController.AddFlags(fs)
}

func (c *Controllers) Complete(rootDir string) error {
	if c.SAController.ServiceAccountKeyFile == "" {
		// use sa.key and auto-generate if not existing
		c.SAController.ServiceAccountKeyFile = filepath.Join(rootDir, "sa.key")
		if _, err := os.Stat(c.SAController.ServiceAccountKeyFile); os.IsNotExist(err) {
			klog.Background().WithValues("file", c.SAController.ServiceAccountKeyFile).Info("generating service account key file")
			key, err := rsa.GenerateKey(cryptorand.Reader, 4096)
			if err != nil {
				return fmt.Errorf("error generating service account private key: %w", err)
			}

			encoded, err := keyutil.MarshalPrivateKeyToPEM(key)
			if err != nil {
				return fmt.Errorf("error converting service account private key to PEM format: %w", err)
			}
			if err := keyutil.WriteKey(c.SAController.ServiceAccountKeyFile, encoded); err != nil {
				return fmt.Errorf("error writing service account private key file %q: %w", c.SAController.ServiceAccountKeyFile, err)
			}
		} else if err != nil {
			return fmt.Errorf("error checking service account key file %q: %w", c.SAController.ServiceAccountKeyFile, err)
		}
	}

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
	if saErrs := c.SAController.Validate(); saErrs != nil {
		errs = append(errs, saErrs...)
	}

	return errs
}
