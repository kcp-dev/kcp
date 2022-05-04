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
	"os"
	"path/filepath"

	"github.com/spf13/pflag"

	apiserveroptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/component-base/config"
	"k8s.io/component-base/logs"

	proxyoptions "github.com/kcp-dev/kcp/pkg/proxy/options"
)

type Options struct {
	SecureServing  apiserveroptions.SecureServingOptionsWithLoopback
	Authentication Authentication
	Proxy          proxyoptions.Options
	Logs           *logs.Options

	RootDirectory string
}

func NewOptions() *Options {
	o := &Options{
		SecureServing:  *apiserveroptions.NewSecureServingOptions().WithLoopback(),
		Authentication: *NewAuthentication(),
		Proxy:          *proxyoptions.NewOptions(),
		Logs:           logs.NewOptions(),

		RootDirectory: ".kcp",
	}

	// Default to -v=2
	o.Logs.Config.Verbosity = config.VerbosityLevel(2)

	// override all the things
	o.SecureServing.BindPort = 443
	o.SecureServing.ServerCert.CertDirectory = ""
	o.SecureServing.ServerCert.PairName = "apiserver" // we want to reuse the apiserver certs by default
	return o
}

func (o *Options) AddFlags(fs *pflag.FlagSet) {
	o.SecureServing.AddFlags(fs)
	o.Authentication.AddFlags(fs)
	o.Proxy.AddFlags(fs)

	o.Logs.AddFlags(fs)

	fs.StringVar(&o.RootDirectory, "root-directory", o.RootDirectory, "Root directory.")
}

func (o *Options) Complete() error {
	if err := o.Proxy.Complete(); err != nil {
		return err
	}

	if !filepath.IsAbs(o.RootDirectory) {
		pwd, err := os.Getwd()
		if err != nil {
			return err
		}
		o.RootDirectory = filepath.Join(pwd, o.RootDirectory)
	}

	if len(o.SecureServing.ServerCert.CertDirectory) == 0 {
		o.SecureServing.ServerCert.CertDirectory = o.RootDirectory
	}
	if !filepath.IsAbs(o.SecureServing.ServerCert.CertDirectory) {
		o.SecureServing.ServerCert.CertDirectory = filepath.Join(o.RootDirectory, o.SecureServing.ServerCert.CertDirectory)
	}

	if err := o.SecureServing.MaybeDefaultWithSelfSignedCerts("localhost", []string{"kubernetes.default.svc", "kubernetes.default", "kubernetes"}, nil); err != nil {
		return err
	}

	return nil
}

func (o *Options) Validate() []error {
	var errs []error

	errs = append(errs, o.SecureServing.Validate()...)
	errs = append(errs, o.Authentication.Validate()...)
	errs = append(errs, o.Proxy.Validate()...)

	return errs
}
